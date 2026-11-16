# Local evidence check

Start a Laya server on loopback at `http://127.0.0.1:8000/v1/systemone`. Set the approved corpus root when starting Rakitsu. Use the same environment on `rakitsu serve` for builder runs.

```sh
RAKITSU_RETRIEVAL_ROOT="$PWD/examples/jev/11-retrieval-laya" \
  rakitsu run examples/jev/11-retrieval-laya/config.yaml \
  "How long do duplicate invoice refunds take?" --no-hub
```

Retrieval uses local BM25 on UTF-8 Markdown and text, with relative paths and line ranges for each passage. The Decision receives the question and passages and returns a typed judgment alongside the evidence. A search with no matches returns empty evidence without calling Laya.

Choose a dedicated root that contains only approved documents. The YAML directory is relative to that root. The index is built on each run and is limited to 1 MiB of text, 64 KiB per file, and 2,048 entries. Each passage covers up to eight lines and 1,024 bytes. Pipeline results can be saved in CLI session checkpoints.
