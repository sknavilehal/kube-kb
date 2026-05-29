#!/usr/bin/env python3
"""
RAG query service.

Usage:
    uvicorn main:app --reload --port 8080

Endpoints:
    POST /query   { "question": "..." }
    GET  /health
"""

import os
from pathlib import Path
from typing import Any

import yaml
from fastapi import FastAPI, HTTPException
from langchain_ollama import OllamaEmbeddings, OllamaLLM
from langchain_postgres import PGVector
from langchain_core.prompts import PromptTemplate
from pydantic import BaseModel

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

def load_config() -> dict:
    config_path = os.environ.get(
        "CONFIG_PATH",
        str(Path(__file__).parent.parent / "config.yaml"),
    )
    with open(config_path) as f:
        return yaml.safe_load(f)


cfg = load_config()

# ---------------------------------------------------------------------------
# LangChain components (initialised once at startup)
# ---------------------------------------------------------------------------

embeddings = OllamaEmbeddings(
    model=cfg["embedding_model"],
    base_url=cfg["ollama_base_url"],
)

vector_store = PGVector(
    embeddings=embeddings,
    collection_name="documents",
    connection=cfg["postgres_dsn"],
    use_jsonb=True,
)

llm = OllamaLLM(
    model=cfg["llm_model"],
    base_url=cfg["ollama_base_url"],
)

RAG_PROMPT = PromptTemplate.from_template(
    """You are a helpful assistant. Use only the context below to answer the question.
If the context does not contain enough information, say "I don't have enough information to answer that."

Context:
{context}

Question: {question}

Answer:"""
)

# ---------------------------------------------------------------------------
# FastAPI app
# ---------------------------------------------------------------------------

app = FastAPI(title="KubeKB RAG Service", version="0.1.0")


class QueryRequest(BaseModel):
    question: str
    top_k: int | None = None


class Source(BaseModel):
    source: str
    start_index: int | None = None


class QueryResponse(BaseModel):
    answer: str
    sources: list[Source]


@app.get("/health")
def health() -> dict[str, Any]:
    return {"status": "ok", "llm_model": cfg["llm_model"], "embedding_model": cfg["embedding_model"]}


@app.post("/query", response_model=QueryResponse)
def query(req: QueryRequest) -> QueryResponse:
    if not req.question.strip():
        raise HTTPException(status_code=400, detail="question must not be empty")

    top_k = req.top_k or cfg.get("top_k", 5)

    # 1. Embed the question and retrieve top-k similar chunks
    retriever = vector_store.as_retriever(search_kwargs={"k": top_k})
    docs = retriever.invoke(req.question)

    if not docs:
        return QueryResponse(answer="No relevant documents found in the knowledge base.", sources=[])

    # 2. Build context string
    context = "\n\n---\n\n".join(d.page_content for d in docs)

    # 3. Run the LLM
    prompt = RAG_PROMPT.format(context=context, question=req.question)
    answer = llm.invoke(prompt)

    # 4. Build source list (deduplicated, preserving order)
    seen: set[str] = set()
    sources: list[Source] = []
    for d in docs:
        src = d.metadata.get("source", "unknown")
        if src not in seen:
            seen.add(src)
            sources.append(Source(
                source=src,
                start_index=d.metadata.get("start_index"),
            ))

    return QueryResponse(answer=answer.strip(), sources=sources)
