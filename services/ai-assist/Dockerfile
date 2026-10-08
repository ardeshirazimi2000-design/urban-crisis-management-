FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /srv
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt && useradd --uid 10001 --no-create-home app
COPY app ./app
USER 10001
EXPOSE 8000
# Default: Kafka worker. The HTTP API runs with: uvicorn app.main:app --host 0.0.0.0 --port 8000
CMD ["python", "-m", "app.worker"]
