/* Japanese catalog copy. IDs and download paths stay stable across languages. */
window.RakitsuJa = {
  ui: {
    privateCatalog: '非公開カタログ', testedRecipes: 'テスト済みレシピ',
    eyebrow: 'オーケストレーション・ルーティングマップ ↗', titleMain: '問題から始める。', titleSub: 'パターンで解決する。',
    lede: 'Rakitsuで実用的な作業を進めるための、小さくバージョン管理されたレシピです。入力から検証済みの出力までの経路を確認し、テスト済みの正確なバンドルをダウンロードできます。',
    noteLabel: 'この地図の読み方', noteCopy: '左側の問題を選びます。各ルートはレシピに進み、その下にある共通パターンを表示します。',
    searchLabel: 'レシピを検索', placeholder: '問題、レシピ、パターンを検索…', filterLabel: 'カテゴリで絞り込む', allCategories: 'すべてのカテゴリ', loading: 'レシピを読み込み中…',
    routes: 'ルート', problem: '問題', recipe: 'レシピ', pattern: 'パターン', graphLabel: '問題・レシピ・パターンのグラフ', graphHint: 'ノードを選択して経路を確認 →',
    showing: (shown, total) => `${total}件中${shown}件のレシピ`, noMatch: '一致するレシピがありません。「review」「parallel」「support」などで試してください。',
    clearSearch: '検索をクリアすると、すべてのレシピを確認できます。', testedBuild: 'Rakitsuテスト済みビルド', runFolder: '展開したフォルダから実行', download: 'ZIPをダウンロード', downloadProject: 'プロジェクトZIPをダウンロード', playthrough: 'テスト済みプレイスルー', exampleOutput: '出力例', recipes: n => `${n}件のレシピ`,
    privateFooter: '検索はこの端末内で処理されます。レシピの表示、プレビューのクリック、ZIP のクリックを訪問者 ID なしで集計します。', switchJapanese: '日本語に切り替え', switchEnglish: '英語に切り替え', couldNotLoad: 'カタログを読み込めませんでした。ローカルサーバー経由でこのプレビューを開いてください。'
  },
  categories: {
    'Writing & research': '執筆・調査', 'Operations': '業務運用', 'Engineering': 'エンジニアリング',
    'Reliability': '信頼性', 'Logistics': '物流', 'Procurement': '調達',
    'People operations': '人事・総務', 'Facilities': '設備管理', 'Data operations': 'データ運用',
    'Product design': 'プロダクトデザイン', 'Security operations': 'セキュリティ運用',
    'Games': 'ゲーム', 'Manufacturing': '製造', 'Business operations': '事業運営', 'Research & development': '研究開発'
  },
  recipes: {
    'evidence-brief': { problem:'調査結果を短く共有したい', title:'根拠付きブリーフ', category:'運用', description:'複数の資料を確認し、出典付きの意思決定用ブリーフにまとめます。', pattern:'調査→検証→要約', flow:'入力 → 調査 → 根拠チェック → ブリーフ' },
    'support-triage': { problem:'問い合わせを正しい担当へ振り分けたい', title:'サポート・トリアージ', category:'カスタマーサポート', description:'問い合わせを分類し、優先度と次の対応を明確にします。', pattern:'分類→ルーティング→返信案', flow:'問い合わせ → 分類 → 担当割当 → 返信案' },
    'change-review': { problem:'変更の影響を安全に確認したい', title:'変更レビュー', category:'エンジニアリング', description:'変更内容を検査し、リスク、テスト、承認条件を整理します。', pattern:'検査→リスク評価→承認', flow:'差分 → 検査 → リスク → 判定' },
    'incident-handoff': { problem:'障害対応を次の担当へ引き継ぎたい', title:'障害ハンドオフ', category:'運用', description:'障害の事実、対応、未解決点を次の担当が使える形に整えます。', pattern:'収集→時系列化→引き継ぎ', flow:'ログ → 事実確認 → 時系列 → 引き継ぎ' },
    'shipment-exceptions': { problem:'配送例外を早く解決したい', title:'配送例外の解決', category:'運用', description:'遅延や紛失の情報を突き合わせ、顧客対応まで進めます。', pattern:'照合→判断→顧客対応', flow:'配送記録 → 照合 → 対応判断 → 顧客連絡' },
    'invoice-match': { problem:'請求書と発注を照合したい', title:'請求書照合', category:'財務', description:'請求書、発注書、納品記録を確認して不一致を発見します。', pattern:'抽出→照合→例外処理', flow:'書類 → 抽出 → 照合 → 例外一覧' },
    'onboarding-handoff': { problem:'新入社員の準備漏れをなくしたい', title:'オンボーディング引き継ぎ', category:'人事・総務', description:'必要なアカウント、端末、案内を確認し、担当者へ渡します。', pattern:'チェックリスト→割当→確認', flow:'入社情報 → 確認 → 担当割当 → 完了報告' },
    'site-maintenance': { problem:'設備の修理依頼に優先順位を付けたい', title:'設備修理の振り分け', category:'エンジニアリング', description:'水漏れや機器不良などの依頼を設備管理ルールに沿って振り分けます。', pattern:'計画→実行→検証', flow:'修理依頼 → 分類 → 担当と期限の決定' },
    'data-import-review': { problem:'データ取り込みの品質を確認したい', title:'データ取り込みレビュー', category:'エンジニアリング', description:'取り込みデータの形式、欠損、重複を検査し、修正案を出します。', pattern:'検査→不備分類→修正', flow:'データ → 検査 → 不備 → 修正案' },
    'accessibility-review': { problem:'画面のアクセシビリティを改善したい', title:'アクセシビリティレビュー', category:'エンジニアリング', description:'画面と操作を確認し、利用者が困る点と具体的な修正を示します。', pattern:'確認→基準照合→修正案', flow:'画面 → 操作確認 → 基準照合 → 修正案' },
    'access-review': { problem:'権限付与がルール通りか確認したい', title:'アクセス権レビュー', category:'セキュリティ', description:'申請、所属、期限をルールと照合し、許可・拒否・追加確認に分類します。', pattern:'ルール照合→判定→監査記録', flow:'申請 → ルール照合 → 判定 → 記録' },
    'invoice-payment-holds': { problem:'支払い保留を正しく処理したい', title:'請求支払い保留', category:'財務', description:'不一致や承認不足を調べ、支払い保留の理由と次の担当を記録します。', pattern:'例外検出→承認確認→保留', flow:'請求書 → 検出 → 承認確認 → 保留記録' },
    'shipment-customer-escalations': { problem:'配送トラブルの顧客対応を揃えたい', title:'配送顧客エスカレーション', category:'カスタマーサポート', description:'約束した納期と配送状況を照合し、連絡が必要な注文の一覧を作成します。', pattern:'状況確認→方針決定→返信', flow:'納期と配送状況 → 照合 → 連絡対象一覧' },
    'contractor-access-cleanup': { problem:'契約終了後も残っているアクセス権を見つけたい', title:'委託先アクセス整理', category:'セキュリティ', description:'契約情報とアクセス一覧を照合し、契約が終了しても有効な権限を、人が確認するための一覧にまとめます。', pattern:'照合→期限確認→無効化', flow:'契約一覧 → 権限照合 → 確認用CSV' },
    'night-shift': { problem:'夜勤中のNPC対応を安全に進めたい', title:'ナイトシフト・オペレーション', category:'運用', description:'ゲーム内の出来事を観測し、NPCエージェントが協力して判断と対応を進めます。', pattern:'観測→協調→検証', flow:'出来事 → エージェント協調 → 対応 → 結果' },
    'image-to-ui-verified-lessons': { problem:'画像から再利用できるUIを作りたい', title:'画像からUIへの検証済みレッスン', category:'クリエイティブ制作', description:'画像と仕様から共通部品で画面を実装し、Chromeで測定した不具合を直して手順をプロジェクトに記録します。', pattern:'観察→実装→比較検証', flow:'参照画像 → 分解 → 実装 → 比較' }
  }
};
