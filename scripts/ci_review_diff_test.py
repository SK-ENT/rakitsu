#!/usr/bin/env python3
"""Offline coverage for bounded PR review diff staging."""
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHUNKER = ROOT / "scripts/chunk-review-diff.py"
CI_REVIEW = ROOT / "scripts/ci-review.sh"
AI_REVIEW_WORKFLOW = ROOT / ".github/workflows/ai-review.yml"


class ChunkReviewDiffTests(unittest.TestCase):
    def test_ready_for_review_triggers_full_review_mode(self):
        workflow = AI_REVIEW_WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("types: [opened, synchronize, reopened, ready_for_review]", workflow)
        self.assertIn('EVENT_ACTION" = "synchronize"', workflow)
        self.assertIn('echo "mode=full"', workflow)

    def test_iteration_budget_matches_both_review_configs(self):
        script = CI_REVIEW.read_text(encoding="utf-8")
        script_budget = int(re.search(r"MAX_REVIEW_ITERATIONS=(\d+)", script).group(1))
        for name in ("review-config-openai.yaml", "review-config-openai-full.yaml"):
            config = (ROOT / "scripts" / name).read_text(encoding="utf-8")
            agent_config = config.split("\nagents:\n", 1)[1]
            match = re.search(r"max_iterations:\s*(\d+)", agent_config)
            self.assertIsNotNone(match, name)
            self.assertEqual(int(match.group(1)), script_budget, name)

    def run_chunker(self, diff: bytes, limit: int):
        with tempfile.TemporaryDirectory() as td:
            source = Path(td) / "input.diff"
            output = Path(td) / "chunks"
            source.write_bytes(diff)
            result = subprocess.run(
                ["python3", str(CHUNKER), str(source), str(output), "--limit", str(limit)],
                capture_output=True,
            )
            files = sorted(output.glob("chunk-*.diff")) if output.exists() else []
            return result, [p.read_bytes() for p in files]

    def test_aggregate_over_old_cap_is_fully_chunked(self):
        pieces = [f"diff --git a/file{i} b/file{i}\n".encode() + b"+" + b"x" * 7000 + b"\n" for i in range(20)]
        diff = b"".join(pieces)
        result, chunks = self.run_chunker(diff, 15000)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertGreater(len(diff), 120000)
        self.assertEqual(b"".join(chunks), diff)
        self.assertTrue(all(len(chunk) <= 15000 for chunk in chunks))

    def test_single_file_over_limit_fails_explicitly(self):
        diff = b"diff --git a/huge b/huge\n" + b"x" * 100
        result, chunks = self.run_chunker(diff, 50)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"single file diff exceeds", result.stderr)
        self.assertEqual(chunks, [])

    def test_ci_review_uses_short_prompt_and_staged_chunks(self):
        with tempfile.TemporaryDirectory() as td:
            repo = Path(td) / "repo"
            repo.mkdir()
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "config", "user.name", "Review Test"], check=True)
            subprocess.run(["git", "-C", str(repo), "config", "user.email", "review@example.test"], check=True)
            subprocess.run(["git", "-C", str(repo), "config", "commit.gpgsign", "false"], check=True)
            source_file = repo / "large.txt"
            source_file.write_text("base\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(repo), "add", "large.txt"], check=True)
            subprocess.run(["git", "-C", str(repo), "commit", "-qm", "base"], check=True)
            base = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
            for index in range(5):
                (repo / f"large-{index}.txt").write_text("x" * 35000 + "\n", encoding="utf-8")
            hostile_review_dir = repo / ".review-diff.attacker"
            hostile_review_dir.mkdir()
            (hostile_review_dir / "MANIFEST.txt").write_text("forged manifest\n", encoding="utf-8")
            (hostile_review_dir / "chunk-0001.diff").write_text("forged diff\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(repo), "add", "large-*.txt"], check=True)
            subprocess.run(["git", "-C", str(repo), "add", ".review-diff.attacker"], check=True)
            subprocess.run(["git", "-C", str(repo), "commit", "-qam", "large change"], check=True)
            head = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()

            fake = Path(td) / "rakitsu"
            fake.write_text(
                "#!/usr/bin/env python3\n"
                "import json,os,re,sys\n"
                "args=sys.argv[1:]\n"
                "assert len(args[1]) < 1000, len(args[1])\n"
                "workdir=args[args.index('--workdir')+1]\n"
                "sessions_dir=args[args.index('--sessions-dir')+1] if '--sessions-dir' in args else os.environ['FAKE_RAKITSU_SESSIONS']\n"
                "os.makedirs(sessions_dir,exist_ok=True)\n"
                "full=args[1].endswith('review-config-openai-full.yaml')\n"
                "if full:\n"
                " assert os.path.isdir(os.path.join(workdir,'repo'))\n"
                " assert os.path.isdir(os.path.join(workdir,'review-diff'))\n"
                " assert os.path.isfile(os.path.join(workdir,'repo','.review-diff.attacker','MANIFEST.txt'))\n"
                " diff_root=os.path.join(workdir,'review-diff')\n"
                "else:\n"
                " diff_root=os.path.join(workdir,'review-diff')\n"
                "found=[]\n"
                "manifest=None\n"
                "for root,dirs,files in os.walk(diff_root):\n"
                " for name in files:\n"
                "  if name.endswith('.diff'): found.append((name,open(os.path.join(root,name),'rb').read()))\n"
                "  if name=='MANIFEST.txt': manifest=open(os.path.join(root,name),encoding='utf-8').read()\n"
                "assert sum(len(data) for _,data in found) > 120000\n"
                "assert manifest is not None\n"
                "listed=[line.split('\\t',1)[0] for line in manifest.splitlines()]\n"
                "assert set(listed)=={name for name,_ in found}\n"
                "config=open(args[1],encoding='utf-8').read().split('\\nagents:\\n',1)[1]\n"
                "match=re.search(r'max_iterations:\\s*(\\d+)',config)\n"
                "assert match and int(match.group(1)) >= len(listed)+2\n"
                "full_config=open(args[1],encoding='utf-8').read()\n"
                "if full:\n"
                " read_tool=full_config.split('- name: read_file',1)[1].split('- name: list_dir',1)[0]\n"
                " list_tool=full_config.split('- name: list_dir',1)[1].split('- name: search_code',1)[0]\n"
                " search_tool=full_config.split('- name: search_code',1)[1].split('agents:',1)[0]\n"
                " assert 'allowed_paths: [\"repo\", \"review-diff\"]' in read_tool\n"
                " assert 'allowed_paths: [\"repo\"]' in list_tool\n"
                " assert 'allowed_paths: [\"repo\"]' in search_tool\n"
                " assert 'prefix repository paths with repo/' in args[2]\n"
                "required=['review-diff/MANIFEST.txt']+['review-diff/'+name for name in listed]\n"
                "read_mode=os.environ.get('FAKE_RAKITS_READ_CHUNKS','all')\n"
                "read_paths=required[:-1] if read_mode=='omit-last' else required[1:] if read_mode=='omit-manifest' else required\n"
                "path_style=os.environ.get('FAKE_RAKITS_READ_PATH_STYLE','relative')\n"
                "if path_style=='dot': read_paths=['./'+path for path in read_paths]\n"
                "if path_style=='absolute': read_paths=[os.path.join(workdir,path) for path in read_paths]\n"
                "events=[]\n"
                "for index,path in enumerate(read_paths):\n"
                " call_id='call-'+str(index)\n"
                " events.append({'agent_name':'Reviewer','event_type':'TOOL_CALL_START','payload':{'tool_call_id':call_id,'tool_name':'read_file','arguments':{'path':path}}})\n"
                " end={'tool_call_id':call_id,'tool_name':'read_file','output':'read ok','output_truncated':False}\n"
                " if read_mode=='failed-last' and path==required[-1]: end['error']='read failed'\n"
                " if read_mode=='truncated-last' and path==required[-1]: end['output_truncated']=True\n"
                " if read_mode=='empty-output-last' and path==required[-1]: end['output']=''\n"
                " events.append({'agent_name':'Reviewer','event_type':'TOOL_CALL_END','payload':end})\n"
                "session_status='error' if read_mode=='session-error' else 'success'\n"
                "events.append({'event_type':'SESSION_END','payload':{'status':session_status}})\n"
                "with open(os.path.join(sessions_dir,'session.jsonl'),'w',encoding='utf-8') as stream:\n"
                " if read_mode=='malformed': stream.write('not json\\n')\n"
                " for event in events: stream.write(json.dumps(event)+'\\n')\n"
                "response=os.environ.get('FAKE_RAKITSU_RESPONSE','NO FINDINGS')\n"
                "print('Result (took 1s)')\nprint('━━━━━━━━━━━━━━━━')\nprint(response)\nprint('━━━━━━━━━━━━━━━━')\n",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            env = dict(
                os.environ,
                OPENAI_API_KEY="test-only",
                RAKITSU_BIN=str(fake),
                FAKE_RAKITSU_SESSIONS=str(Path(td) / "fallback-sessions"),
            )
            for mode in ("full", "incremental"):
                for path_style in ("relative", "dot", "absolute"):
                    success_env = dict(env, FAKE_RAKITS_READ_PATH_STYLE=path_style)
                    output = Path(td) / f"{mode}-{path_style}-result.json"
                    result = subprocess.run(
                        [str(CI_REVIEW), mode, base, head, str(output), base],
                        cwd=repo,
                        env=success_env,
                        check=False,
                        capture_output=True,
                        text=True,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("No issues found", output.read_text(encoding="utf-8"))

            incomplete_env = dict(env, FAKE_RAKITSU_RESPONSE="[max_iterations reached — synthesized answer]\\n\\nNO FINDINGS")
            for read_mode in ("omit-last", "omit-manifest"):
                missing_read_env = dict(env, FAKE_RAKITS_READ_CHUNKS=read_mode)
                for mode in ("full", "incremental"):
                    output = Path(td) / f"{mode}-{read_mode}-result.json"
                    result = subprocess.run(
                        [str(CI_REVIEW), mode, base, head, str(output), base],
                        cwd=repo,
                        env=missing_read_env,
                        check=False,
                        capture_output=True,
                        text=True,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                    body = output.read_text(encoding="utf-8")
                    self.assertIn("Review incomplete", body)
                    self.assertNotIn("No issues found", body)

            for read_mode in ("failed-last", "truncated-last", "empty-output-last", "session-error", "malformed"):
                invalid_session_env = dict(env, FAKE_RAKITS_READ_CHUNKS=read_mode)
                output = Path(td) / f"full-{read_mode}-result.json"
                result = subprocess.run(
                    [str(CI_REVIEW), "full", base, head, str(output), base],
                    cwd=repo,
                    env=invalid_session_env,
                    check=False,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                body = output.read_text(encoding="utf-8")
                self.assertIn("Review incomplete", body)
                self.assertNotIn("No issues found", body)

            incomplete_output = Path(td) / "incomplete-result.json"
            result = subprocess.run(
                [str(CI_REVIEW), "incremental", base, head, str(incomplete_output), base],
                cwd=repo,
                env=incomplete_env,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Review incomplete", incomplete_output.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
