# InfraOps Autopilot MVP

InfraOps Autopilot is a hackathon-style IAM automation service that turns Jira access requests into deterministic Terraform changes.

The project is split into two parts:

- `app.py`: a FastAPI service that receives Jira webhook payloads, uses an LLM to reason over the repository, decides which Terraform file and mutation path to use, then opens a GitHub pull request with the change.
- `main.go`: a Go CLI (`tf-engine`) that applies Terraform mutations through `hashicorp/hcl/v2/hclwrite` token/AST operations instead of regex or raw string replacement.

## Current Flow

1. Jira sends a webhook to `POST /webhook`.
2. The FastAPI service clones the target GitHub repo into a temp directory.
3. It scans all `.tf` files and sends the ticket plus repo context to Gemini.
4. The LLM returns one or more normalized IAM requests.
5. For each request, the backend builds a Go mutation plan and runs `./tf-engine <plan-file>`.
6. The backend commits the changed Terraform files, pushes a feature branch, and opens a pull request.

## Repository Layout

- [app.py](/Users/sharaonabishek/PycharmProjects/InfraOps-MVP/app.py): FastAPI webhook receiver and orchestration logic
- [main.go](/Users/sharaonabishek/PycharmProjects/InfraOps-MVP/main.go): Go Terraform mutation engine
- [go.mod](/Users/sharaonabishek/PycharmProjects/InfraOps-MVP/go.mod): Go module definition
- [main.tf](/Users/sharaonabishek/PycharmProjects/InfraOps-MVP/main.tf): local Terraform sample/scratch file

## What The Go Engine Supports

The engine currently supports these plan actions:

- `UPDATE_BLOCK`
  Appends to an existing list-style attribute.

- `APPEND_NEW_MODULE`
  Appends a new `module` block to an existing Terraform file.

- `CREATE_NEW_TOSET_RESOURCE`
  Creates a new native Terraform `resource` block with:
  `for_each = toset([...])` and `member = each.value`

- `APPEND_TO_TOSET`
  Finds an existing native `resource` block and appends a member inside `for_each = toset([...])`.

- `APPEND_TO_MODULE_MAP`
  Finds a module map such as `iam_bindings` and either:
  appends a member to an existing role key list, or creates the role key if it does not exist.

### Idempotency

The engine exits cleanly with `IDEMPOTENT_SKIP` when the requested member already exists in:

- an existing list
- a `toset([...])` expression
- a module IAM map entry

## FastAPI Decision Model

The backend currently asks the LLM to classify each IAM request into one of these paths:

- `UPDATE_MODULE_MAP`
- `UPDATE_EXISTING_RESOURCE`
- `CREATE_NEW_RESOURCE`

That decision then maps to Go engine actions:

- `UPDATE_MODULE_MAP` -> `APPEND_TO_MODULE_MAP`
- `UPDATE_EXISTING_RESOURCE` -> `APPEND_TO_TOSET`
- `CREATE_NEW_RESOURCE` -> `CREATE_NEW_TOSET_RESOURCE`

## Setup

### Prerequisites

- Python 3.11+
- Go 1.25+
- Git
- A compiled local `tf-engine` binary
- Access to the target GitHub repo
- A Gemini API key
- A GitHub token with repo write access

### Environment Variables

Create a `.env` file with:

```env
GEMINI_API_KEY=your_gemini_api_key
GITHUB_TOKEN=your_github_token
```

`GITHUB_REPO` is currently hardcoded in [app.py](/Users/sharaonabishek/PycharmProjects/InfraOps-MVP/app.py) as:

```python
GITHUB_REPO = "sharaonsathanandam/mock-client-poc-repo-final"
```

Update that value if you want to point at a different repository.

### Build The Go Engine

```bash
go build -o tf-engine .
```

### Install Python Dependencies

This repo does not currently include a pinned `requirements.txt` or `pyproject.toml`, but the code uses:

- `fastapi`
- `uvicorn`
- `pydantic`
- `python-dotenv`
- `langchain-core`
- `langchain-google-genai`
- `PyGithub`

Install them in your preferred environment before running the API.

## Run The API

```bash
uvicorn app:app --reload
```

The webhook endpoint is:

```text
POST /webhook
```

## Example Mutation Behaviors

### Existing native IAM resource

If a matching native Terraform IAM resource already exists, the engine updates:

```hcl
for_each = toset([
  "user:existing@company.com"
])
```

by appending the new member inside the `toset(...)` list.

### Existing module IAM map

If a module contains:

```hcl
iam_bindings = {
  "roles/pubsub.publisher" = [
    "serviceAccount:existing-user@company.com"
  ]
}
```

the engine can:

- append a member to the existing role key, or
- create the role key with a new list if it does not exist

### No existing IAM resource block

If the target file exists but no matching block exists, the backend can instruct the engine to create a new native resource block and append it to that file.

## Current Limitations

- The engine expects the target Terraform file to already exist. It does not currently create a brand-new `.tf` file if the file path is missing.
- The backend depends heavily on LLM routing quality for choosing the correct target file.
- `GITHUB_REPO` is hardcoded instead of being configured through environment variables.
- The FastAPI file still contains some older schema definitions that are not the source of truth for the live Go plan format.
- There is no automated test suite in the repo yet.

## Suggested Next Steps

- Add first-class support for creating a missing IAM file when a new service is onboarded.
- Add unit tests around the Go token mutation helpers.
- Add integration tests for the end-to-end Jira -> plan -> Terraform mutation flow.
- Move repository settings and branch strategy into environment variables.
- Add a pinned Python dependency manifest.
