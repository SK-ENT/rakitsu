# Rakitsu 開発ガイド

macOS および Windows 向けのセットアップと開発の完全ガイドです。

---

## 目次

- [前提条件](#前提条件)
- [クイックスタート](#クイックスタート)
- [詳細セットアップ](#詳細セットアップ)
  - [macOS](#macos-セットアップ)
  - [Windows](#windows-セットアップ)
- [ビルド](#ビルド)
- [開発ワークフロー](#開発ワークフロー)
- [フロントエンド開発](#フロントエンド開発)
- [実行とテスト](#実行とテスト)
- [Docker 開発](#docker-開発)
- [環境変数](#環境変数)
- [プロジェクト構成](#プロジェクト構成)
- [Makefile リファレンス](#makefile-リファレンス)
- [トラブルシューティング](#トラブルシューティング)

---

## 前提条件

| 依存関係 | バージョン | 必須 | 備考 |
|---------|-----------|------|------|
| Go | 1.25+ | はい | バックエンドのコンパイル |
| Node.js | 20.19+（または 22.12+） | はい | フロントエンドのビルド — Vite 7 の最小要件 |
| npm | 9+ | はい | Node.js に付属 |
| Git | 2.x+ | はい | ビルド時のバージョン注入 |
| Make | 任意 | 推奨 | ビルド自動化（macOS は標準搭載、Windows は別途インストール） |
| air | 最新 | 任意 | Go のホットリロード |
| golangci-lint | 最新 | 任意 | Go の高度なリンティング |
| Docker | 20+ | 任意 | コンテナ化されたビルドとテスト |
| Ollama | 最新 | 任意 | 下記クイックスタートで使うローカルモデルバックエンド（[ollama.com/download](https://ollama.com/download)）。クラウドプロバイダを使う場合は不要 |

---

## クイックスタート

```bash
# 1. クローン
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu

# 2. Go の依存関係をインストール
go mod download

# 3. フロントエンドの依存関係をインストール
cd web && npm install && cd ..

# 4. モデルバックエンドを選択 — ローカル Ollama（キー不要、examples のデフォルト）…
ollama pull llama3.1:8b
#    …またはクラウドの API キーを設定し、example 設定のプロバイダーを切り替える:
# export OPENAI_API_KEY="sk-..."

# 5. ビルド（Web UI 埋め込みバイナリ）
make build-embedded

# 6. 実行
./bin/rakitsu run examples/single/02-single-agent/config.yaml "カレントディレクトリのファイルを一覧表示"
./bin/rakitsu serve --port 9100
```

---

## 詳細セットアップ

### macOS セットアップ

#### 1. Go のインストール

**Homebrew（推奨）：**
```bash
brew install go
```

**手動ダウンロード：**
https://go.dev/dl/ からアーキテクチャに合った `.pkg` をダウンロードしてインストール（Apple Silicon = arm64、Intel = amd64）。

確認：
```bash
go version
# go version go1.25.0 darwin/arm64
```

#### 2. Node.js のインストール

**Homebrew：**
```bash
brew install node
```

**nvm（バージョン管理に推奨）：**
```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.0/install.sh | bash
nvm install 22
nvm use 22
```

確認：
```bash
node --version   # v22.x.x
npm --version    # 10.x.x
```

#### 3. オプションツールのインストール

```bash
# Go 開発用のホットリロード
go install github.com/air-verse/air@latest

# 高度なリンティング
brew install golangci-lint
```

#### 4. クローンとビルド

```bash
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
go mod download
cd web && npm install && cd ..
make build-embedded
```

バイナリは `./bin/rakitsu` に生成されます。

#### macOS 固有の注意事項

- **Xcode コマンドラインツール**: Git と Make に必要です。未インストールの場合は `xcode-select --install` で導入してください。
- **拡張属性**: Makefile は埋め込みフロントエンドに対して `xattr -rc` を実行し、macOS の検疫フラグを除去します。これは自動的に処理されます。
- **Apple Silicon（M1/M2/M3/M4）**: ネイティブ arm64 ビルドが生成されます。Rosetta は不要です。

---

### Windows セットアップ

#### 1. Go のインストール

https://go.dev/dl/ から `.msi` インストーラ（Windows amd64）をダウンロードしてください。

**winget を使用：**
```powershell
winget install GoLang.Go
```

**Chocolatey を使用：**
```powershell
choco install golang
```

確認（インストール後に**新しい**ターミナルを開いてください）：
```powershell
go version
# go version go1.25.0 windows/amd64
```

#### 2. Node.js のインストール

https://nodejs.org/ から LTS 版の `.msi` インストーラをダウンロードしてください。

**winget を使用：**
```powershell
winget install OpenJS.NodeJS.LTS
```

**Chocolatey を使用：**
```powershell
choco install nodejs-lts
```

確認：
```powershell
node --version
npm --version
```

#### 3. Git のインストール

https://git-scm.com/download/win からダウンロードしてください。

**winget を使用：**
```powershell
winget install Git.Git
```

インストール時に「Use Git from the Windows Command Prompt」を選択して PATH に Git を追加してください。

#### 4. Make のインストール（任意だが推奨）

Makefile を使うとビルドが簡単になります。以下のいずれかの方法でインストールしてください。

**方法 A — Chocolatey：**
```powershell
choco install make
```

**方法 B — winget (GnuWin32)：**
```powershell
winget install GnuWin32.Make
```

**方法 C — Make なしで開発：**
Go と npm のコマンドを直接実行できます（[Make なしでのビルド](#make-なしでのビルド) を参照）。

#### 5. オプションツールのインストール

```powershell
# ホットリロード
go install github.com/air-verse/air@latest

# リンティング
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

#### 6. クローンとビルド

**PowerShell：**
```powershell
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
go mod download
cd web; npm install; cd ..
make build-embedded
```

**Make なしの場合：**
```powershell
cd web; npm install; npm run build; cd ..
Remove-Item -Recurse -Force internal\webui\dist -ErrorAction SilentlyContinue
Copy-Item -Recurse web\dist internal\webui\dist
New-Item -ItemType Directory -Force -Path bin | Out-Null
$commit = git rev-parse --short HEAD
go build -ldflags "-X main.Version=v0.2.0-alpha.3.$commit -X main.BuildCommit=$commit" -o bin\rakitsu.exe .\cmd\rakitsu
```

#### Windows 固有の注意事項

- **YAML 設定のパス区切り文字**: YAML ファイルではスラッシュ（`/`）を使用してください。Windows を含む全プラットフォームで動作します。
- **環境変数**: PowerShell では `$env:OPENAI_API_KEY = "sk-..."`、コマンドプロンプトでは `set OPENAI_API_KEY=sk-...` を使用してください。
- **長いパス**: パス長の問題が発生した場合は、Windows のロングパスを有効にしてください：
  ```powershell
  # 管理者として実行
  New-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem" -Name "LongPathsEnabled" -Value 1 -PropertyType DWORD -Force
  ```
- **バイナリ名**: Windows では出力バイナリは `rakitsu.exe` になります。
- **Docker Desktop**: Docker ベースのワークフローに必要です。最高のパフォーマンスを得るには WSL 2 バックエンドを有効にしてください。

---

## ビルド

### 標準ターゲット

| コマンド | 説明 |
|---------|------|
| `make build` | Go バイナリのみビルド → `bin/rakitsu` |
| `make build-embedded` | フロントエンドをビルドしてバイナリに埋め込み（推奨） |
| `make build-all` | クロスコンパイル：linux（amd64+arm64）、darwin（amd64+arm64）、windows（amd64）— 計5ターゲット |
| `make frontend` | Vue.js フロントエンドのみビルド → `web/dist/` |
| `make embed-frontend` | `web/dist/` を Go の埋め込みパスにコピー |
| `make clean` | 全ビルド成果物を削除 |

### バージョンの上書き

```bash
make build VERSION=v0.2.0-beta.1
# 出力: rakitsu version v0.2.0-beta.1+abc1234
```

ビルドコミットハッシュは `git rev-parse --short HEAD` から自動的に注入されます。

### クロスコンパイル

```bash
make build-all
```

生成されるバイナリ：
```
bin/rakitsu-linux-amd64
bin/rakitsu-linux-arm64
bin/rakitsu-darwin-amd64
bin/rakitsu-darwin-arm64
bin/rakitsu-windows-amd64.exe
```

### Make なしでのビルド

Make が利用できない場合（Make 未インストールの Windows）：

```powershell
# フロントエンドのビルド
cd web
npm install
npm run build
cd ..

# フロントエンドの埋め込み
Remove-Item -Recurse -Force internal\webui\dist -ErrorAction SilentlyContinue
Copy-Item -Recurse web\dist internal\webui\dist

# Go バイナリのビルド
New-Item -ItemType Directory -Force -Path bin | Out-Null
$commit = git rev-parse --short HEAD
go build -ldflags "-X main.Version=v0.2.0-alpha.3.$commit -X main.BuildCommit=$commit" -o bin\rakitsu.exe .\cmd\rakitsu
```

---

## 開発ワークフロー

### ホットリロード（Go バックエンド）

```bash
# air のインストール（初回のみ）
go install github.com/air-verse/air@latest

# 自動リビルド付きの開発サーバーを起動
make dev
```

air は `.go` ファイルの変更を監視し、自動的にリビルドします。

### フロントエンド開発サーバー（Vite）

```bash
cd web
npm run dev
```

Vite がホットモジュールリプレースメント（HMR）付きの開発サーバーを起動します（デフォルト: `http://localhost:5173`）。フロントエンドは API コールを Go バックエンドにプロキシします。

### 一般的な開発サイクル

1. **バックエンドの変更**: `make dev`（air）または手動で `make build && ./bin/rakitsu serve`
2. **フロントエンドの変更**: `web/` で `npm run dev` を使用して即座に HMR
3. **統合テスト**: `make build-embedded && ./bin/rakitsu serve`
4. **コミット前**: `make check && make test`

### コードフォーマットとリンティング

```bash
make fmt     # Go コードのフォーマット（gofmt）
make lint    # go vet + golangci-lint
make check   # go mod tidy + verify + vet
```

---

## フロントエンド開発

### セットアップ

```bash
cd web
npm install
```

### スクリプト

| コマンド | 説明 |
|---------|------|
| `npm run dev` | HMR 付き Vite 開発サーバーを起動 |
| `npm run build` | 型チェック（vue-tsc）+ プロダクションビルド |
| `npm run preview` | プロダクションビルドをローカルでプレビュー |

### 主要な技術スタック

- **Vue 3** — `<script setup lang="ts">` と Composition API
- **Vue Flow** — ビジュアルグラフビルダー（ノード、エッジ、ハンドル）
- **Vite 7** — バンドリングと開発サーバー
- **TypeScript 5.9** — strict モード有効

### ディレクトリ構成

```
web/src/
├── components/
│   ├── nodes/          # Vue Flow ノードタイプ（AgentNode、ToolNode 等）
│   ├── builder/        # ビジュアルビルダー UI（VisualBuilder、NodeEditor 等）
│   ├── inspector/      # 実行インスペクター（EventItem、RunInspector）
│   └── debugger/       # デバッグビュー（HierarchyTree、ExecutionGraph 等）
├── composables/        # 再利用ロジック（useEventStream、useYamlExport 等）
├── types/              # Go 構造体に対応する TypeScript 型定義
├── App.vue
└── main.ts
```

---

## 実行とテスト

### CLI コマンド

```bash
# 設定とクエリでエージェントを実行
./bin/rakitsu run examples/single/02-single-agent/config.yaml "プロジェクト構造を分析"

# マルチ実行監視用の SSE ハブを起動
./bin/rakitsu serve --port 9100

# デバッグ SSE サーバー付きで実行
./bin/rakitsu run examples/single/02-single-agent/config.yaml "クエリ" --debug-port 9200

# トレース出力付きで実行（stderr にカラー出力）
./bin/rakitsu run examples/single/02-single-agent/config.yaml "クエリ" --trace

# バージョン表示
./bin/rakitsu version
```

### テストの実行

```bash
# 全 Go テスト
make test

# 特定パッケージ
go test -v ./internal/agent/...
go test -v ./internal/tools/...

# カバレッジ付き
go test -cover ./...

# 競合検出
go test -race ./...
```

### Windows でのテストコマンド

```powershell
# 全テスト
go test -v ./...

# 特定パッケージ
go test -v ./internal/agent/...
```

---

## Docker 開発

> 以下で参照しているルートの `Dockerfile` と `docker-compose.yml` は、Docker
> パッケージングのリリースステップで追加されます。まだ手元のチェックアウトに
> 存在しない場合、ストレス/負荷テストハーネス用の
> `test/stress/docker-compose.yml` は現時点でも利用できます。

### イメージのビルド

```bash
docker build -t rakitsu .
```

### Docker Compose での実行

```bash
# API キーを設定
export OPENAI_API_KEY="sk-..."

# シングルエージェントテスト
docker-compose up test-single

# マルチエージェントテスト
docker-compose up test-multi

# UI サーバー（http://localhost:8080 でアクセス）
docker-compose up test-ui

# Gemini プロバイダーテスト
export GEMINI_API_KEY="..."
docker-compose up test-gemini
```

### Docker Compose サービス一覧

| サービス | 説明 | 必要な環境変数 |
|---------|------|--------------|
| `test-single` | リフレクション付きシングルエージェント | `OPENAI_API_KEY` |
| `test-multi` | マルチエージェントオーケストレーション | `OPENAI_API_KEY` |
| `test-gemini` | Gemini API キー認証 | `GEMINI_API_KEY` |
| `test-gemini-sa` | Gemini サービスアカウント | `GOOGLE_APPLICATION_CREDENTIALS` |
| `test-ui` | ポート 8080 の Web UI | `OPENAI_API_KEY` |

---

## 環境変数

### LLM API キー

| 変数名 | プロバイダー | 必須 |
|--------|------------|------|
| `OPENAI_API_KEY` | OpenAI（GPT-4o 等） | いずれか1つのプロバイダーキーが必要 |
| `ANTHROPIC_API_KEY` | Anthropic（Claude） | いずれか1つのプロバイダーキーが必要 |
| `GEMINI_API_KEY` | Google Gemini | いずれか1つのプロバイダーキーが必要 |
| `GOOGLE_APPLICATION_CREDENTIALS` | Gemini（サービスアカウント） | 任意 |
| `LITELLM_API_KEY` | LiteLLM プロキシ | 任意 |
| `LITELLM_BASE_URL` | LiteLLM ベース URL | 任意 |

### 環境変数の設定方法

**macOS / Linux（bash/zsh）：**
```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."

# 永続化: ~/.zshrc または ~/.bashrc に追加
echo 'export OPENAI_API_KEY="sk-..."' >> ~/.zshrc
```

**Windows PowerShell：**
```powershell
$env:OPENAI_API_KEY = "sk-..."

# 永続化（ユーザーレベル）
[Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-...", "User")
```

**Windows コマンドプロンプト：**
```cmd
set OPENAI_API_KEY=sk-...

:: 永続化
setx OPENAI_API_KEY "sk-..."
```

### YAML 設定での参照

環境変数は YAML 設定ファイルで `${VAR_NAME}` 構文で参照されます：

```yaml
settings:
  api_keys:
    openai: "${OPENAI_API_KEY}"
    anthropic: "${ANTHROPIC_API_KEY}"
  base_urls:
    ollama: "http://localhost:11434/v1"
```

---

## プロジェクト構成

```
rakitsu/
├── cmd/rakitsu/                 # CLI エントリポイント
│   ├── root.go               #   ルートコマンドとグローバルフラグ
│   ├── run.go                #   rakitsu run — エージェント実行
│   ├── serve.go              #   rakitsu serve — SSE ハブ + Web UI
│   └── scaffold.go           #   rakitsu scaffold — 設定テンプレート
├── internal/
│   ├── agent/                # ReAct ループ + オーケストレーター
│   │   ├── agent.go          #   シングルエージェント実行
│   │   └── orchestrator.go   #   マルチエージェント連携
│   ├── config/               # YAML 設定解析
│   │   └── config.go         #   Viper ベースの設定構造体
│   ├── llm/                  # LLM プロバイダー抽象化
│   │   ├── provider.go       #   LLMProvider インターフェース
│   │   ├── openai/           #   OpenAI + Ollama + LiteLLM
│   │   ├── anthropic/        #   Anthropic Claude
│   │   └── gemini/           #   Google Gemini
│   ├── tools/                # ツールインターフェース + 実装
│   │   ├── tool.go           #   Tool インターフェース
│   │   ├── cli/              #   CLI ツール（サンドボックス付き）
│   │   └── fs/               #   ファイルシステムツール（パス制限付き）
│   ├── telemetry/            # イベントバス + 型付きイベント
│   │   └── events.go         #   EventType 定数 + ペイロード
│   ├── server/               # SSE ハブ + API + ランナー
│   │   ├── sse.go            #   SSE サーバー
│   │   ├── hub.go            #   ハブエンドポイント
│   │   ├── runner.go         #   エージェントランナー
│   │   └── configs.go        #   設定ストア
│   ├── debug/                # デバッガーサブシステム
│   │   ├── controller.go     #   デバッグコントローラー
│   │   ├── replay.go         #   実行リプレイ
│   │   └── export.go         #   設定エクスポート
│   ├── store/                # セッション永続化（JSONL）
│   │   └── store.go
│   └── webui/                # 埋め込みフロントエンド
│       ├── embed.go          #   //go:embed ディレクティブ
│       └── dist/             #   ビルド済みフロントエンド（web/dist からコピー）
├── web/                      # Vue.js フロントエンドソース
│   ├── src/
│   │   ├── components/       #   Vue コンポーネント
│   │   ├── composables/      #   Composition API フック
│   │   └── types/            #   TypeScript 型定義
│   ├── package.json
│   ├── vite.config.ts
│   └── tsconfig.json
├── examples/                 # YAML 設定例
│   ├── single/                #   単一ファイル設定（chat、pipeline、dev-team 等）
│   ├── modular/               #   同じ例を agents/tools/skills ディレクトリに分割したもの
│   ├── providers/             #   プロバイダーごとの設定サンプル
│   └── eval/                  #   評価/ベンチマーク設定
├── test/                     # テスト設定 + ワークスペース
├── docs/                     # ドキュメント
├── go.mod                    # Go モジュール + 依存関係
├── Makefile                  # ビルド自動化
├── Dockerfile                # マルチステージ Docker ビルド
└── docker-compose.yml        # テストサービス定義
```

---

## Makefile リファレンス

| ターゲット | 説明 |
|-----------|------|
| `make build` | Go バイナリをビルド → `bin/rakitsu` |
| `make build-embedded` | フロントエンド + 埋め込み + Go バイナリ |
| `make build-all` | クロスコンパイル（5 ターゲット） |
| `make frontend` | `web/` で `npm install && npm run build` |
| `make embed-frontend` | `web/dist/` → `internal/webui/dist/` にコピー |
| `make frontend-init` | Vue.js プロジェクトを初期化（初回のみ） |
| `make test` | `go test -v ./...` |
| `make lint` | `go vet` + `golangci-lint`（インストール済みの場合） |
| `make fmt` | `gofmt` で Go コードをフォーマット |
| `make check` | `go mod tidy` + `go mod verify` + `go vet` |
| `make install` | バイナリを `$GOPATH/bin` にコピー |
| `make clean` | `bin/`、`web/dist/`、`internal/webui/dist/` を削除 |
| `make run` | ビルド + 実行（`ARGS='...'` で CLI 引数を指定） |
| `make dev` | air でホットリロード |
| `make docs` | Go ドキュメントを生成 |
| `make help` | 全ターゲットを表示 |

---

## トラブルシューティング

### 共通の問題（全プラットフォーム）

**`go mod download` が認証エラーで失敗する：**
```bash
# プロキシ/プライベートモジュールの設定ミスがないか確認
go env GOPROXY GOPRIVATE GONOSUMCHECK
# モジュールキャッシュが破損している場合はクリア
go clean -modcache
go mod download
```

**フロントエンドのビルドが失敗する — `vue-tsc` の型エラー：**
```bash
cd web
rm -rf node_modules package-lock.json
npm install
npm run build
```

**バイナリは起動するが Web UI が空白ページを表示する：**
フロントエンドが埋め込まれていません。以下で再ビルドしてください：
```bash
make build-embedded
```

**LLM API エラー（401/403）：**
API キーが設定されていて有効であることを確認してください：
```bash
echo $OPENAI_API_KEY          # macOS/Linux
echo $env:OPENAI_API_KEY      # Windows PowerShell
```

### macOS 固有の問題

**`make: command not found`：**
```bash
xcode-select --install
```

**ビルド済みバイナリに検疫警告が表示される：**
```bash
xattr -rc ./bin/rakitsu
```

**ポートが既に使用中：**
```bash
lsof -i :9100
kill -9 <PID>
```

### Windows 固有の問題

**`make` が認識されない：**
Chocolatey で Make をインストール（`choco install make`）するか、手動でビルドしてください（[Make なしでのビルド](#make-なしでのビルド) を参照）。

**`go build` が CGO エラーで失敗する：**
Rakitsu はプロダクションビルドに `CGO_ENABLED=0` を使用します。CGO の問題が発生した場合：
```powershell
$env:CGO_ENABLED = "0"
go build -o bin\rakitsu.exe .\cmd\rakitsu
```

**ポートの競合：**
```powershell
netstat -ano | findstr :9100
taskkill /PID <PID> /F
```

**Node.js インストール後に `npm` コマンドが見つからない：**
ターミナルを閉じて再度開いてください。それでも見つからない場合は、Node.js を手動で PATH に追加してください：
```powershell
$env:Path += ";C:\Program Files\nodejs"
```

**改行コードの問題（CRLF vs LF）：**
Git の改行コード処理を設定してください：
```powershell
git config --global core.autocrlf true
```

**PowerShell の実行ポリシーがスクリプトをブロックする：**
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```
