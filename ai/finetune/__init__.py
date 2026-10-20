"""Text-to-text use and fine-tuning of EmbeddingGemma 2 (development machine only).

docs/research/embeddinggemma-2-text-finetuning.md explains what is here and why;
docs/research/qdrant-vector-store.md covers store.py, index.py and vectors.py
(a Qdrant / in-memory vector store behind one interface).
Nothing in this package ships in the AI Worker runtime: embedders.py,
similarity.py, store.py, index.py and vectors.py need only the standard library, server.py and train.py need
sentence-transformers from requirements.txt in a separate virtualenv.
"""
