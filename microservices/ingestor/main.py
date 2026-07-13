from langchain_community.document_loaders import GitLoader
from langchain_text_splitters import RecursiveCharacterTextSplitter
from langchain_ollama import OllamaEmbeddings
from langchain_postgres import PGVector
import tempfile
import logging
import os
import time
import urllib.request
import urllib.error
import json

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# ==================== CONFIGURATION ====================
REPO_URL = os.getenv("REPO_URL")
if not REPO_URL:
    raise ValueError("REPO_URL environment variable is required")

DB_HOST = os.getenv("DB_HOST", "localhost")
DB_USERNAME = os.getenv("DB_USERNAME", "postgres")
DB_PASSWORD = os.getenv("DB_PASSWORD", "postgres")
DB_NAME = os.getenv("DB_NAME", "rag_db")
DB_COLLECTION = os.getenv("DB_COLLECTION", "markdown_docs")

EMBED_MODEL = os.getenv("EMBED_MODEL", "nomic-embed-text") 
LLM_MODEL = os.getenv("LLM_MODEL", "llama3.2:3b")
OLLAMA_BASE_URL = os.getenv("OLLAMA_BASE_URL", "http://localhost:11434")

CONNECTION_STRING = (
    f"postgresql+psycopg://{DB_USERNAME}:{DB_PASSWORD}@{DB_HOST}:5432/{DB_NAME}"
)

def wait_for_postgres(connection_string: str, timeout: int = 60, interval: int = 2) -> None:
    """Wait until Postgres is accepting connections."""
    import psycopg

    deadline = time.time() + timeout
    attempt = 0
    while True:
        attempt += 1
        try:
            conn = psycopg.connect(connection_string)
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
            with urllib.request.urlopen(url, timeout=5) as resp:
                if resp.status == 200:
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
            with urllib.request.urlopen(url, timeout=5) as resp:
                data = json.loads(resp.read().decode())
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
    wait_for_ollama_api(OLLAMA_BASE_URL)
    wait_for_ollama_model(OLLAMA_BASE_URL, EMBED_MODEL)
    logger.info("=== All services ready ===")


def main():
    wait_for_services()
    with tempfile.TemporaryDirectory() as temp_dir:
        try:
            logger.info(f"Cloning repository: {REPO_URL}")
            loader = GitLoader(
                clone_url=REPO_URL,
                repo_path=temp_dir,
                file_filter=lambda path: path.endswith(".md"),
            )
            docs = loader.load()
            logger.info(f"Loaded {len(docs)} markdown documents")

            embeddings = OllamaEmbeddings(
                model=EMBED_MODEL,
                base_url=OLLAMA_BASE_URL,
            )

            text_splitter = RecursiveCharacterTextSplitter(
                chunk_size=1000, chunk_overlap=200
            )
            splits = text_splitter.split_documents(docs)
            logger.info(f"Created {len(splits)} text chunks")

            vectorstore = PGVector(
                embeddings=embeddings,
                connection=CONNECTION_STRING,
                collection_name=DB_COLLECTION,
                pre_delete_collection=True,
                use_jsonb=True,
            )
            vectorstore.add_documents(documents=splits)

            logger.info("=== INGESTION COMPLETE ===")
            logger.info(f"Documents stored in collection: {DB_COLLECTION}")

        except Exception as e:
            logger.error(f"Error during ingestion: {e}")
            raise


if __name__ == "__main__":
    main()
