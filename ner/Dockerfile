# spaCy NER sidecar for Engram
#
# Build args:
#   SPACY_MODEL — spaCy model to download (default: en_core_web_sm)
#
# Env vars at runtime:
#   SPACY_MODEL — override the model (must match what was downloaded)
#   NER_PORT    — port to listen on (default: 5001)

ARG SPACY_MODEL=en_core_web_sm

FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Download model at build time so container starts fast
ARG SPACY_MODEL
RUN python -m spacy download ${SPACY_MODEL}

COPY server.py .

ENV SPACY_MODEL=${SPACY_MODEL}
ENV NER_PORT=5001

EXPOSE 5001

CMD ["python", "server.py"]
