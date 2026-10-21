# Strategy Service (Python) Dockerfile
FROM python:3.11-slim

WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Copy requirements first for better caching
COPY requirements.txt .

# Install Python dependencies
RUN pip install --no-cache-dir --require-hashes -r requirements.txt

# Copy source code - copy everything to /app
COPY . /app

# Set Python path
ENV PYTHONPATH=/app

# Never run as root. uid 1000 matches the usual host user, so the dev compose bind mount
# (./strategy:/app) stays writable for journals and checkpoints.
RUN useradd --uid 1000 --create-home app && chown -R app /app
USER app

EXPOSE 8000

# Run with correct path
CMD ["python", "/app/app/main.py"]
