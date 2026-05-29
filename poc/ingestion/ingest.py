#!/usr/bin/env python3
"""
Ingestion pipeline: clone/pull a GitHub repo, split docs into chunks,
embed via Ollama, and upsert into pgvector.

Usage:
    python ingest.py                        # uses ../config.yaml
    python ingest.py --config /path/to/config.yaml
    python ingest.py --repo https://github.com/org/repo --branch main
"""

import argparse
import os
import shutil
import tempfile
from pathlib import Path

import yaml
from git import Repo
from langchain.text_splitter import RecursiveCharacterTextSplitter
from langchain_core.documents import Document
from langchain_ollama import OllamaEmbeddings
from langchain_postgres import PGVector

SUPPORTED_EXTENSIONS = {".md", ".txt", ".rst", ".mdx"}


def load_config(path: str) -> dict:
    with open(path) as f:
        cfg = yaml.safe_load(f)
    # Allow env var override for sensitive values
    if token := os.environ.get("GITHUB_TOKEN"):
        cfg["github_token"] = token
    return cfg


def clone_or_pull(repo_url: str, branch: str, token: str | None, dest: str) -> Repo:
    if token:
        # Inject token into HTTPS URL
        repo_url = repo_url.replace("https://", f"https://oauth2:{token}@")

    if Path(dest).exists():
        print(f"[ingest] Pulling latest from {branch}...")
        repo = Repo(dest)
        repo.remotes.origin.pull(branch)
    else:
        print(f"[ingest] Cloning {repo_url} (branch: {branch})...")
        repo = Repo.clone_from(repo_url, dest, branch=branch, depth=1)

    commit = repo.head.commit.hexsha[:8]
    print(f"[ingest] HEAD commit: {commit}")
    return repo


def load_documents(repo_dir: str) -> list[Document]:
    docs = []
    for path in Path(repo_dir).rglob("*"):
        if path.suffix.lower() not in SUPPORTED_EXTENSIONS:
            continue
        # Skip hidden dirs (.git, .github, etc.)
        if any(part.startswith(".") for part in path.parts):
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="ignore").strip()
        except Exception:
            continue
        if not text:
            continue
        docs.append(Document(
            page_content=text,
            metadata={"source": str(path.relative_to(repo_dir))},
        ))
    print(f"[ingest] Loaded {len(docs)} source files")
    return docs


def split_documents(docs: list[Document], chunk_size: int, chunk_overlap: int) -> list[Document]:
    splitter = RecursiveCharacterTextSplitter(
        chunk_size=chunk_size,
        chunk_overlap=chunk_overlap,
        add_start_index=True,
    )
    chunks = splitter.split_documents(docs)
    print(f"[ingest] Split into {len(chunks)} chunks "
          f"(size={chunk_size}, overlap={chunk_overlap})")
    return chunks


def upsert(chunks: list[Document], cfg: dict) -> None:
    embeddings = OllamaEmbeddings(
        model=cfg["embedding_model"],
        base_url=cfg["ollama_base_url"],
    )

    print(f"[ingest] Connecting to pgvector at {cfg['postgres_dsn']!r}...")
    store = PGVector(
        embeddings=embeddings,
        collection_name="documents",
        connection=cfg["postgres_dsn"],
        use_jsonb=True,
    )

    print(f"[ingest] Upserting {len(chunks)} chunks (this may take a while)...")
    # PGVector.add_documents handles create-table-if-not-exists internally
    ids = store.add_documents(chunks)
    print(f"[ingest] Done. Upserted {len(ids)} vectors.")


def main():
    parser = argparse.ArgumentParser(description="KubeKB ingestion pipeline")
    parser.add_argument("--config", default=str(Path(__file__).parent.parent / "config.yaml"))
    parser.add_argument("--repo", help="Override github_repo from config")
    parser.add_argument("--branch", help="Override github_branch from config")
    args = parser.parse_args()

    cfg = load_config(args.config)
    if args.repo:
        cfg["github_repo"] = args.repo
    if args.branch:
        cfg["github_branch"] = args.branch

    repo_dir = str(Path(tempfile.gettempdir()) / "kube-kb-repo")

    clone_or_pull(
        repo_url=cfg["github_repo"],
        branch=cfg["github_branch"],
        token=cfg.get("github_token"),
        dest=repo_dir,
    )

    docs = load_documents(repo_dir)
    if not docs:
        print("[ingest] No documents found — check SUPPORTED_EXTENSIONS or repo path.")
        return

    chunks = split_documents(docs, cfg["chunk_size"], cfg["chunk_overlap"])
    upsert(chunks, cfg)


if __name__ == "__main__":
    main()
