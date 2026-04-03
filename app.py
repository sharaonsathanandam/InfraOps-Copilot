import json
import os
import subprocess
import tempfile
from functools import lru_cache
from typing import Any, Literal, Dict, List
from fastapi import FastAPI
from pydantic import BaseModel, Field
from langchain_google_genai import ChatGoogleGenerativeAI
from langchain_core.messages import HumanMessage, SystemMessage
from github import Github
from dotenv import load_dotenv


load_dotenv()
api_key = os.getenv("GEMINI_API_KEY")
GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
GITHUB_REPO = "sharaonsathanandam/mock-client-poc-repo-final"

def run_cmd(cmd: list, cwd: str):
    """Helper to run shell commands and capture both output streams"""
    result = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        # Combine stdout and stderr so we can actually see "nothing to commit"
        full_error = f"{result.stdout}\n{result.stderr}".strip()
        raise Exception(f"Command failed: {' '.join(cmd)}\nError: {full_error}")
    return result.stdout

class TargetIdentifier(BaseModel):
    target_file_path: str = Field(description="The exact relative path of the file to modify.")
    action_type: Literal["UPDATE_EXISTING_RESOURCE", "CREATE_NEW_RESOURCE", "UPDATE_MODULE_MAP"] = Field(
        description="Choose how to mutate the file based on the existing code."
    )
    block_name: str = Field(description="The name of the resource or module block. If CREATE_NEW_RESOURCE, output 'none'.")
    map_attribute: str = Field(description="If UPDATE_MODULE_MAP, the name of the map variable (e.g., 'iam_bindings'). Otherwise 'none'.")


class IAMRequest(BaseModel):
    member_id: str = Field(description="The user's email formatted as 'user:...' or 'serviceAccount:...' or 'group:..'")
    terraform_resource_type: str = Field(description="The exact native Terraform resource required.")
    resource_attributes: Dict[str, str] = Field(description="Mandatory arguments (e.g., role, topic, dataset_id).")
    target: TargetIdentifier


class StructuredTicket(BaseModel):
    conflict_detected: bool = Field(description="Set to True if summary and description contradict.")
    conflict_reason: str = Field(description="Explanation of the conflict.")
    project_env: str = Field(description="Extract the exact project name.")
    requests: List[IAMRequest] = Field(description="A list of EVERY distinct IAM permission requested in the ticket.")


class MutationSpec(BaseModel):
    attribute: str = Field(description="The exact attribute name to change")
    operation: Literal["list_append", "overwrite", "block_insert"] = Field(
        description="list_append, overwrite, or block_insert")
    value: str = Field(description="The new value to inject")


class CRUDSchema(BaseModel):
    file_path: str = Field(description="Exact path to the file to be mutated")
    action: Literal["CREATE_FILE", "APPEND_BLOCK", "UPDATE_BLOCK"] = Field(
        description="CREATE_FILE, APPEND_BLOCK, or UPDATE_BLOCK")
    target_identifier: TargetIdentifier
    mutations: MutationSpec


class WebhookResponse(BaseModel):
    issue_key: str | None
    event_type: str | None
    plan: CRUDSchema


app = FastAPI(title="InfraOps Autopilot", version="0.1.0")


@lru_cache(maxsize=1)
def get_llm():
    llm = ChatGoogleGenerativeAI(model="gemini-2.5-flash", temperature=0,google_api_key=api_key )
    return llm

def get_repo_context(repo_dir: str) -> str:
    """Scans the repository and concatenates all .tf files for the LLM."""
    context_parts = []
    for root, _, files in os.walk(repo_dir):
        # Skip the hidden git folder to save space
        if ".git" in root:
            continue
        for file in files:
            if file.endswith(".tf"):
                filepath = os.path.join(root, file)
                rel_path = os.path.relpath(filepath, repo_dir)
                with open(filepath, "r") as f:
                    content = f.read()
                    context_parts.append(f"--- START FILE: {rel_path} ---\n "
                                         f"{content}\n"
                                         f"--- END FILE: {rel_path} ---\n ")

    return "\n".join(context_parts)

def flatten_jira_text(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, list):
        parts = [flatten_jira_text(item) for item in value]
        return "\n".join(part for part in parts if part)
    if isinstance(value, dict):
        parts: list[str] = []
        text = value.get("text")
        if isinstance(text, str) and text.strip():
            parts.append(text.strip())
        content = value.get("content")
        if content is not None:
            nested = flatten_jira_text(content)
            if nested:
                parts.append(nested)
        return "\n".join(part for part in parts if part)
    return str(value)


def extract_jira_context(payload: dict[str, Any]) -> dict[str, Any]:
    issue = payload.get("issue", {}) if isinstance(payload, dict) else {}
    fields = issue.get("fields", {}) if isinstance(issue, dict) else {}

    labels = fields.get("labels")
    if not isinstance(labels, list):
        labels = []

    components = fields.get("components")
    component_names = []
    if isinstance(components, list):
        for component in components:
            if isinstance(component, dict):
                name = component.get("name")
                if isinstance(name, str) and name.strip():
                    component_names.append(name.strip())

    return {
        "event_type": payload.get("webhookEvent") or payload.get("issue_event_type_name"),
        "issue_key": issue.get("key"),
        "issue_type": ((fields.get("issuetype") or {}).get("name") if isinstance(fields.get("issuetype"), dict) else None),
        "summary": fields.get("summary"),
        "description": flatten_jira_text(fields.get("description")),
        "labels": labels,
        "priority": ((fields.get("priority") or {}).get("name") if isinstance(fields.get("priority"), dict) else None),
        "project_key": ((fields.get("project") or {}).get("key") if isinstance(fields.get("project"), dict) else None),
        "components": component_names,
        "raw_issue_fields": fields,
    }


def build_messages(payload: dict[str, Any]) -> list[SystemMessage | HumanMessage]:
    jira_context = extract_jira_context(payload)
    human_prompt = (
        "Convert this Jira webhook payload into a single Terraform mutation plan.\n\n"
        "Return only the structured object required by the schema.\n\n"
        f"{json.dumps(jira_context, indent=2, default=str)}"
    )

    return [
        SystemMessage(content=SYSTEM_PROMPT),
        HumanMessage(content=human_prompt),
    ]


@app.post("/webhook")
async def handle_jira_webhook(payload: dict):
    description = payload.get("issue", {}).get("fields", {}).get("description", "")
    summary = payload.get("issue", {}).get("fields", {}).get("summary", "")
    ticket_id = payload.get("issue", {}).get("key", "Jira-123")  # Fallback for curl tests

    with tempfile.TemporaryDirectory() as repo_dir:
        try:
            # 1. Clone the repo into the temp folder
            print("Cloning GitHub repository...")
            clone_url = f"https://oauth2:{GITHUB_TOKEN}@github.com/{GITHUB_REPO}.git"
            run_cmd(["git", "clone", clone_url, "."], cwd=repo_dir)

            # 2. Create a new feature branch
            branch_name = f"feature/{ticket_id}-iam-update"
            run_cmd(["git", "checkout", "-b", branch_name], cwd=repo_dir)

            # 3. Scan the entire repository
            plan_file_path = os.path.join(repo_dir, "plan.yaml")
            print("Scanning repository structure...")
            repo_tf_context = get_repo_context(repo_dir)

            prompt = f"""
            Extract the strict infrastructure requirements from this Jira ticket.
            Ticket Summary: {summary}
            Ticket Description: {description}
            
            CRITICAL ROUTING RULES FOR TARGET FILE:
            You must determine the correct `target_file_path` based on these strict rules:
            1. Project-level IAM updates -> Use `nonprod/<project_name>/iam/iam-members.tf`
            2. Folder-level IAM updates -> Use `nonprod/iam-members.tf`
            3. BigQuery Dataset-level updates -> Use `nonprod/<project_name>/bq/<dataset_name>.tf` (or the closest matching dataset file in the bq folder).
            4. Other resource-level updates (Pub/Sub, Compute, etc.) -> Use a file with iam in the file name under the location `<project_name>/<service_name>`

            REPOSITORY CONTEXT (All .tf files):
            {repo_tf_context}
        
            CRITICAL INFRASTRUCTURE RULES - DO NOT DEVIATE:
            1. Extract the user email, project, and requested role from the ticket.
            2. Extract ALL requested access permissions. If the ticket asks for multiple roles or multiple users, you MUST create a separate item in the `requests` list for each distinct combination of user + role + resource.
            3. AUDIT FOR CONFLICTS: Compare the TICKET SUMMARY and TICKET DESCRIPTION. If they contradict each other in any way (e.g., different permissions, different projects, different users), set `conflict_detected` to True and explain the issue in `conflict_reason`. Do not try to guess which one is right.
            4. Determine the EXACT `terraform_resource_type` needed (e.g., google_pubsub_topic_iam_member).
            5. For EACH request in the list:
               - Determine the EXACT `terraform_resource_type` needed (e.g., google_storage_bucket_iam_member).
               - Determine the logical grouping for this request (Format: <role>_<project>).
               - Extract the exact `resource_attributes` needed (e.g., role, bucket).
               - Look for an existing `resource` block that perfectly matches BOTH the resource type AND the specific attributes you extracted.
               - Analyze the specific file you identified in `target_file_path`:
                 - SCENARIO A: If an existing `module` block manages IAM for this target using a map (like `iam_bindings = {{...}}`), set target.action_type = 'UPDATE_MODULE_MAP', target.block_name to the module label, and target.map_attribute to 'iam_bindings'.
                 - SCENARIO B: If an existing native `resource` block exists for this target, set target.action_type = 'UPDATE_EXISTING_RESOURCE' and target.block_name to its label.
                 - SCENARIO C: If NO block exists for this specific target, set target.action_type = 'CREATE_NEW_RESOURCE' and target.block_name to 'none'.
            """

            print("Extracting parameters from Jira ticket.")

            # 4. Grab the raw LLM, then attach the Fuzzy Parser schema!
            llm = get_llm().with_structured_output(StructuredTicket)
            extracted_data = llm.invoke(prompt)

            if extracted_data.conflict_detected:
                msg = f"Ticket rejected due to ambiguity: {extracted_data.conflict_reason} Please update the Jira ticket so the summary and description match."
                print(f"REJECTED: {msg}")
                return {"status": "error", "message": msg}

            # 5. THE UPSERT LOGIC: Does the module exist?
            # Use the exact path the LLM determined based on the rules
            modified_files = set()  # We use a set to keep track of unique files changed

            for index, req in enumerate(extracted_data.requests):
                print(f"\n--- Processing Sub-Request {index + 1}/{len(extracted_data.requests)} ---")

                dynamic_tf_file_path = req.target.target_file_path
                absolute_tf_file_path = os.path.join(repo_dir, dynamic_tf_file_path)
                modified_files.add(dynamic_tf_file_path)

                # --- NEW: Day 2 Bootstrapping ---
                if not os.path.exists(absolute_tf_file_path):
                    print(f"Bootstrapping new path -> {dynamic_tf_file_path}")
                    # 1. Create the new folders
                    os.makedirs(os.path.dirname(absolute_tf_file_path), exist_ok=True)

                    # 2. Create a blank .tf file for the Go engine to parse
                    with open(absolute_tf_file_path, "w") as f:
                        f.write("// IAM Configuration auto-generated by InfraOps Autopilot\n")
                # --------------------------------
                # Default empty payload
                go_plan = {}

                # Build the Go Payload for THIS specific request
                if req.target.action_type == "UPDATE_MODULE_MAP":
                    print(
                        f"Instructing Go Engine to APPEND user to module map '{req.target.block_name}' in {dynamic_tf_file_path}")
                    go_plan = {
                        "file_path": absolute_tf_file_path,
                        "action": "APPEND_TO_MODULE_MAP",
                        "module_name": req.target.block_name,
                        "map_attribute": req.target.map_attribute,
                        "role_key": req.resource_attributes.get("role"),
                        "new_member": req.member_id
                    }
                elif req.target.action_type == "UPDATE_EXISTING_RESOURCE":
                    print(f"Instructing Go Engine to APPEND user to {req.target.block_name} in {dynamic_tf_file_path}")
                    go_plan = {
                        "file_path": absolute_tf_file_path,
                        "action": "APPEND_TO_TOSET",
                        "resource_type": req.terraform_resource_type,
                        "resource_name": req.target.block_name,
                        "new_member": req.member_id
                    }
                else:
                    print(f"Instructing Go Engine to CREATE resource {req.target.block_name} in {dynamic_tf_file_path}")
                    go_plan = {
                        "file_path": absolute_tf_file_path,
                        "action": "CREATE_NEW_TOSET_RESOURCE",
                        "resource_type": req.terraform_resource_type,
                        "resource_name": req.target.block_name,
                        "attributes": req.resource_attributes,
                        "initial_member": req.member_id
                    }

                    print(f"Instructing Go Engine to CREATE {req.target.block_name} in {dynamic_tf_file_path}")
                    go_plan = {
                        "file_path": absolute_tf_file_path,
                        "action": "CREATE_NEW_TOSET_RESOURCE",
                        "resource_type": req.terraform_resource_type,
                        "resource_name": req.target.block_name,
                        "attributes": req.resource_attributes,
                        "initial_member": req.member_id
                    }

                # Don't forget to save plan.json and fire the Go subprocess here!
                import json
                with open(plan_file_path, "w") as f:
                    json.dump(go_plan, f, indent=2)

                print("Firing the Go AST Engine...")
                try:
                    result = subprocess.run(
                        ["./tf-engine", plan_file_path],
                        check=True,
                        capture_output=True,
                        text=True
                    )
                    if "IDEMPOTENT_SKIP" in result.stdout:
                        msg = f"No action needed. {extracted_data.member_id} already has the requested access."
                        print(f"SUCCESS (SKIPPED): {msg}")
                        return {"status": "success", "message": msg}

                except subprocess.CalledProcessError as e:
                    error_msg = e.stderr if e.stderr else e.stdout
                    print(f"FAILED! Go Engine crashed with output:\n{error_msg}")
                    return {"status": "error", "message": f"Go Engine failed: {error_msg}"}

            # 6. Commit and Push to GitHub
            print("Changes applied! Committing and pushing to GitHub...")
            run_cmd(["git", "config", "user.email", "infraops-bot@company.com"], cwd=repo_dir)
            run_cmd(["git", "config", "user.name", "InfraOps Autopilot"], cwd=repo_dir)
            for file_path in modified_files:
                run_cmd(["git", "add", file_path], cwd=repo_dir)
            run_cmd(["git", "commit", "-m", f"InfraOps-Copilot update for {ticket_id}: {summary}"], cwd=repo_dir)
            run_cmd(["git", "push", "origin", branch_name], cwd=repo_dir)

            # 7. Create the Pull Request via API
            print("Creating Pull Request...")
            g = Github(GITHUB_TOKEN)
            repo = g.get_repo(GITHUB_REPO)
            pr = repo.create_pull(
                title=f"[{ticket_id}:{summary}]",
                body=f"InfraOps-Copilot update for {ticket_id}: {summary}",
                head=branch_name,
                base="master"
            )

            success_msg = f"SUCCESS! PR created: {pr.html_url}"
            print(success_msg)
            return {"status": "success", "message": success_msg, "pr_url": pr.html_url}

        except Exception as e:
            print(f"PIPELINE FAILED: {str(e)}")
            return {"status": "error", "message": str(e)}

if __name__ == "__main__":
    import uvicorn

    uvicorn.run("app:app", host="0.0.0.0", port=8000, reload=True)
