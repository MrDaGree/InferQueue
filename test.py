import os
import json
import time
from openai import OpenAI

# Initialize OpenAI client
client = OpenAI(base_url="http://localhost:8080/v1", api_key="")

# Upload to OpenAI file API
batch_file = client.files.create(
    file=open("batchinput.jsonl", "rb"),
    purpose="batch"
)
print(batch_file)

batch_job = client.batches.create(
    input_file_id=batch_file.id,
    endpoint="/v1/chat/completions",
    completion_window="24h"
)

while True:
    batch_job = client.batches.retrieve(batch_job.id)
    if batch_job.status != "completed":
        time.sleep(10)
    else:
        print(f"job {batch_job.id} is done")
        break