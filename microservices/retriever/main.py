from langchain_postgres import PGVector
from langchain_ollama import OllamaEmbeddings, ChatOllama
from langchain_core.prompts import ChatPromptTemplate
from langchain_core.runnables import RunnablePassthrough
from langchain_core.output_parsers import StrOutputParser
import os
import time
import httpx
import logging
from contextlib import asynccontextmanager
from fastapi import FastAPI
import uvicorn
import psycopg

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# ==================== CONFIGURATION ====================
DB_HOST = os.getenv("DB_HOST", "localhost")
DB_USERNAME = os.getenv("DB_USERNAME", "postgres")
DB_PASSWORD = os.getenv("DB_PASSWORD", "postgres")
DB_NAME = os.getenv("DB_NAME", "rag_db")
DB_COLLECTION = os.getenv("DB_COLLECTION", "markdown_docs")

EMBED_MODEL = os.getenv("EMBED_MODEL", "nomic-embed-text")
LLM_MODEL = os.getenv("LLM_MODEL", "llama3.2:3b")
DEFAULT_TOP_K = int(os.getenv("RETRIEVER_TOP_K", "6"))
OLLAMA_BASE_URL = os.getenv("OLLAMA_BASE_URL", "http://localhost:11434")

CONNECTION_STRING = (
    f"postgresql+psycopg://{DB_USERNAME}:{DB_PASSWORD}@{DB_HOST}:5432/{DB_NAME}"
)

def wait_for_postgres(connection_string: str, timeout: int = 60, interval: int = 2) -> None:
    """Wait until Postgres is accepting connections."""

    deadline = time.time() + timeout
    attempt = 0
    while True:
        attempt += 1
        try:
            conn = psycopg.connect(connection_string.replace("postgresql+psycopg://", "postgresql://", 1))
            conn.close()
            logger.info("Postgres is ready")
            return
        except Exception as e:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise TimeoutError(
                    f"Postgres not ready after {timeout}s: {e}"
                )
            logger.info(f"Waiting for Postgres (attempt {attempt}, {remaining:.0f}s remaining): {e}")
            time.sleep(interval)


def wait_for_ollama_api(base_url: str, timeout: int = 120, interval: int = 2) -> None:
    """Wait until the Ollama HTTP API is reachable."""
    url = f"{base_url.rstrip('/')}/api/tags"
    deadline = time.time() + timeout
    attempt = 0
    while True:
        attempt += 1
        try:
            resp = httpx.get(url, timeout=5)
            if resp.status_code == 200:
                logger.info("Ollama API is ready")
                return
        except Exception as e:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise TimeoutError(
                    f"Ollama API not ready after {timeout}s: {e}"
                )
            logger.info(f"Waiting for Ollama API (attempt {attempt}, {remaining:.0f}s remaining): {e}")
            time.sleep(interval)


def wait_for_ollama_model(base_url: str, model: str, interval: int = 5) -> None:
    """Wait until the specified model is available in Ollama (polls indefinitely)."""
    url = f"{base_url.rstrip('/')}/api/tags"
    attempt = 0
    while True:
        attempt += 1
        try:
            resp = httpx.get(url, timeout=5)
            data = resp.json()
            available = [m.get("name", "") for m in data.get("models", [])]
            # Ollama model names may include a tag (e.g. "nomic-embed-text:latest");
            # match on the base name or the full name.
            if any(m == model or m.startswith(f"{model}:") for m in available):
                logger.info(f"Ollama model '{model}' is ready")
                return
            logger.info(
                f"Waiting for Ollama model '{model}' (attempt {attempt}). "
                f"Available: {available or '(none yet)'}"
            )
        except Exception as e:
            logger.info(f"Waiting for Ollama model '{model}' (attempt {attempt}): {e}")
        time.sleep(interval)


def wait_for_services() -> None:
    logger.info("=== Waiting for dependent services ===")
    wait_for_postgres(CONNECTION_STRING)
    #wait_for_ollama_api(OLLAMA_BASE_URL)
    #wait_for_ollama_model(OLLAMA_BASE_URL, EMBED_MODEL)
    #wait_for_ollama_model(OLLAMA_BASE_URL, LLM_MODEL)
    logger.info("=== All services ready ===")


def check_db_connection():
    """Check if the database connection can be established."""
    try:
        conn = psycopg.connect(CONNECTION_STRING.replace("postgresql+psycopg://", "postgresql://", 1))
        conn.close()
        logger.info("Database connection successful")
    except Exception as e:
        logger.error(f"Database connection failed: {e}")
        raise

def check_inference_service():
    """Check if the Ollama inference service is reachable."""
    try:
        url = f"{OLLAMA_BASE_URL.rstrip('/')}/api/tags"
        resp = httpx.get(url, timeout=5)
        if resp.status_code == 200:
            logger.info("Ollama inference service is reachable")
        else:
            raise ConnectionError(f"Ollama API returned status code {resp.status_code}")
    except Exception as e:
        logger.error(f"Ollama inference service check failed: {e}")
        raise

@asynccontextmanager
async def lifespan(app: FastAPI):
    wait_for_services()
    yield


app = FastAPI(title="RAG Query API", lifespan=lifespan)


def format_docs(docs):
    """Format retrieved documents for the LLM prompt."""
    formatted = []
    for doc in docs:
        source = doc.metadata.get("source", doc.metadata.get("file_path", "unknown"))
        formatted.append(f"[Source: {source}]\n{doc.page_content}")
    return "\n\n".join(formatted)


def get_retriever():
    embeddings = OllamaEmbeddings(
        model=EMBED_MODEL,
        base_url=OLLAMA_BASE_URL
    )
    vectorstore = PGVector(
        embeddings=embeddings,
        connection=CONNECTION_STRING,
        collection_name=DB_COLLECTION,
        use_jsonb=True,
    )
    return vectorstore.as_retriever(search_kwargs={"k": DEFAULT_TOP_K})


def get_rag_chain():
    retriever = get_retriever()
    llm = ChatOllama(
        model=LLM_MODEL,
        temperature=0,
        base_url=OLLAMA_BASE_URL
    )

    prompt = ChatPromptTemplate.from_template(
        """You are a helpful assistant that answers questions.
Use ONLY the following retrieved context to answer the question.
If the answer is not in the context, say: "I don't have enough information in the repository to answer that."

Context:
{context}

Question: {question}

Answer:"""
    )

    rag_chain = (
        {
            "context": retriever | format_docs,
            "question": RunnablePassthrough()
        }
        | prompt
        | llm
        | StrOutputParser()
    )
    return rag_chain

@app.get("/health")
async def health():
    check_db_connection()
    check_inference_service()

    return {"status": "ok"}

@app.get("/query")
async def query(question: str):
    rag_chain = get_rag_chain()
    answer = rag_chain.invoke(question)
    return {"answer": answer.strip()}


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000)
