# Use a lightweight, stable Python base image
FROM python:3.11-slim

# Set the working directory inside the container
WORKDIR /app

# Install Git (Crucial for the repository cloning/pushing steps)
RUN apt-get update && \
    apt-get install -y git && \
    rm -rf /var/lib/apt/lists/*

# Copy requirements first to leverage Docker layer caching
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the rest of your application code (Python script, Go binary, etc.)
COPY . .

# NOTE: If your Go binary needs explicit execution permissions, uncomment the line below
# and replace 'go_engine' with your actual binary's filename.
# RUN chmod +x ./go_engine

# Expose the port Uvicorn will run on
EXPOSE 8000

# Start the FastAPI server and bind it to all network interfaces (0.0.0.0)
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]