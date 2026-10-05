(function(){
var VERSIONS=[],DEFAULT_VER="",DATA={},COVERS={},TAGS=[];
var REPO="https://github.com/SK-ENT/rakitsu";
var UI={
 en:{ref:"Spec Reference",version:"Version",view:"View",simple:"Simple",detail:"Detail",
  h1:"How Rakitsu works, and what it does today",
  lead:"A teaching reference for Rakitsu, a YAML-configured multi-agent system written in Go. Each section explains the idea in plain words, the research behind it, how Rakitsu implements it, and where it stops. Diagrams are live: use Pause or Step on any of them. Simple shows the idea and the picture. Detail adds the full explanation.",
  learn:"Learn",refhead:"Spec reference for this release",refs:"References",
  notice:"This release has no doc changes. Showing the docs for %DOC%.",
  noref:"No per-feature reference for this release.",
  gate:"Not in this release: this link points to a feature that %TAG% does not have.",gateGo:"Open it in %TAG%",gateNone:"It is not in any documented release yet.",
  head:{Command:"Command",Description:"Description"}},
 ja:{ref:"仕様リファレンス",version:"バージョン",view:"表示",simple:"簡易",detail:"詳細",
  h1:"Rakitsu のしくみと、現時点でできること",
  lead:"Go で書かれた、YAML で設定するマルチエージェントシステム Rakitsu の学習用リファレンスです。各セクションで、考え方、背景にある研究、Rakitsu での実装、そして限界を説明します。図は動きます。どの図でも一時停止と 1 ステップ実行を使えます。「簡易」は考え方と図、「詳細」は完全な説明を表示します。",
  learn:"学ぶ",refhead:"このリリースの仕様リファレンス",refs:"参考文献",
  notice:"このリリースにはドキュメントの変更がありません。%DOC% のドキュメントを表示しています。",
  noref:"このリリースには機能ごとのリファレンスがありません。",
  gate:"このリリースにはありません: このリンクの機能は %TAG% には含まれていません。",gateGo:"%TAG% で開く",gateNone:"ドキュメントのあるどのリリースにもまだありません。",
  head:{}}
};
var HEADJA={"Command":"コマンド","Description":"説明","Flag":"フラグ","Root key":"ルートキー","Key":"キー","Type":"種別","Default":"既定値","Endpoint":"エンドポイント","Key (settings.providers.<name>)":"キー (settings.providers.<名前>)","Key (settings.wake)":"キー (settings.wake)","Strategy":"戦略","Status":"状況","Protocol":"プロトコル","Where it lives":"実装箇所"};
var st={lang:"en",view:"detail",ver:DEFAULT_VER},saved0=null;
try{var s0=localStorage.getItem("rk-spec");if(s0){var o0=JSON.parse(s0);if(o0.lang)st.lang=o0.lang;if(o0.view)st.view=o0.view;saved0=o0.ver||null;}}catch(e){}
function save(){try{localStorage.setItem("rk-spec",JSON.stringify(st));}catch(e){}}
function $(id){return document.getElementById(id);}
var FOOT=$("foot"); /* regDom renames this element id to "license", so keep the reference */
function el(tag,cls,txt){var e=document.createElement(tag);if(cls)e.className=cls;if(txt!=null)e.textContent=txt;return e;}
function L(o){return o&&typeof o==="object"?(o[st.lang]||o.en):o;}
function T(a,b){return st.lang==="ja"?b:a;}
function docOf(v){return Object.prototype.hasOwnProperty.call(COVERS,v)?COVERS[v]:v;}
function data(v){return DATA[docOf(v)]||{features:[],meta:{}};}
function tagKey(t){var m=/^v(\d+)\.(\d+)\.(\d+)(?:-(alpha|beta|rc)\.(\d+))?$/.exec(t);if(!m)return [0,0,0,0,0];var rank={alpha:0,beta:1,rc:2};return [+m[1],+m[2],+m[3],m[4]?rank[m[4]]:3,+(m[5]||0)];}
function tagCmp(a,b){var x=tagKey(a),y=tagKey(b);for(var i=0;i<Math.max(x.length,y.length);i++){var d=(y[i]||0)-(x[i]||0);if(d)return d;}return 0;}
/* Version gating: an item with a needs key is shown only when the docs for the selected release have that feature id. */
function has(need){var d=data(st.ver);if(!need)return true;
 return d.features.some(function(f){return f.id===need;});}
/* Which feature would a (possibly aliased) link id need? Used only to explain a dead deep link; ids are never renamed. */
function needOf(raw){var id=Object.prototype.hasOwnProperty.call(ALIAS,raw)?ALIAS[raw]:raw;
 if(id==="card-long-running-monitor"||id==="rough-wake-monitors")return "wake";
 var m=/^(?:ref-)?(wake|monitors)(?:-|$)/.exec(id);return m?m[1]:null;}
function codeCell(s){return el("code",null,s);}
function sampleLink(path){var a=el("a","sample-link",T("Open runnable sample","実行可能なサンプルを開く"));a.href=REPO+"/tree/"+encodeURIComponent(st.ver)+"/"+path.split("/").map(encodeURIComponent).join("/");a.target="_blank";a.rel="noopener noreferrer";a.setAttribute("aria-label",T("Open sample in GitHub: "+path,"GitHub でサンプルを開く: "+path));return a;}
function tog(g,c,on){g.classList.toggle(c,!!on);}

/* ---------- static links ----------
   Grammar: #<lang>.<id> or #<id>. It lives only in mkLink/parseToken so it can map to real paths (/en/wake) later.
   The release version is NOT part of the token; the real site will use one path per release tag. */
/*LINKS-BEGIN*/
var REG={list:[],map:{}};
var ALIAS={"react":"agent-loop","flow":"data-flow","orch":"orchestration","cando":"can-do","status":"works-today","refs":"references","f-react":"agent-loop","f-flow":"data-flow","f-orch":"orchestration","f-wake":"wake","f-memory":"memory","f-protocols":"protocols","f-cando":"can-do","f-status":"works-today","f-refs":"references","wake-alarm-tick":"wake-tick-8","wake-change-tick":"wake-tick-5","wake-repeat-tick":"wake-tick-9","wake-quiet-tick":"wake-tick-1"};
["cli","config","providers","tools","orchestrator","monitors","hub","sessions"].forEach(function(k){ALIAS[k]="ref-"+k;ALIAS["f-"+k+"-ref"]="ref-"+k;ALIAS[k+"-ref"]="ref-"+k;});
ALIAS["wake-ref"]="ref-wake";ALIAS["memory-ref"]="ref-memory";ALIAS["f-wake-ref"]="ref-wake";ALIAS["f-memory-ref"]="ref-memory";
function mkLink(lang,id){return "#"+lang+"."+id;}
function canon(raw){
 if(Object.prototype.hasOwnProperty.call(REG.map,raw))return raw;
 if(Object.prototype.hasOwnProperty.call(ALIAS,raw)&&Object.prototype.hasOwnProperty.call(REG.map,ALIAS[raw]))return ALIAS[raw];
 return null;
}
function parseToken(h){
 var t=String(h||"").replace(/^#/,""),lang=null,m=/^(en|ja)\.(.*)$/.exec(t);
 if(m){lang=m[1];t=m[2];}
 return {lang:lang,raw:t,id:canon(t)};
}
/*LINKS-END*/
window.RakitsuLinks={mkLink:mkLink,parseToken:parseToken}; /* read-only handle for tests/validate.mjs */
var pendingScroll=null,hlTimer=null,msgTimer=null;
function regDom(id,kind,label,elm){
 if(Object.prototype.hasOwnProperty.call(REG.map,id))return false;
 var e={id:id,kind:kind,label:label,el:elm,go:null};REG.map[id]=e;REG.list.push(e);elm.id=id;return true;
}
function regItem(id,label,elm,go){
 if(Object.prototype.hasOwnProperty.call(REG.map,id))return false;
 var e={id:id,kind:"diagram",label:label,el:elm,go:go};REG.map[id]=e;REG.list.push(e);return true;
}
function lab(name,en,ja){var n=typeof name==="object"?name:{en:name,ja:name};return {en:n.en+(en||""),ja:(n.ja||n.en)+(ja||"")};}
function slug(x){return String(x).toLowerCase().replace(/<[^>]*>|\[[^\]]*\]/g," ").replace(/[^a-z0-9_]+/g,"-").replace(/^-+|-+$/g,"");}
function rowKey(k){
 var f=String(k).split(/\s+\/\s+|,/)[0];
 var tk=f.trim().split(/\s+/).filter(function(t){return t&&t!=="rakitsu"&&!/^[A-Z][A-Z_]*$/.test(t)&&!/^<.*>$/.test(t)&&!/^\[.*\]$/.test(t);});
 return slug(tk.length?tk.join(" "):f)||"row";
}
function userHash(id){try{history.replaceState(null,"",mkLink(st.lang,id));}catch(e){}}
function reveal(id){
 var e=REG.map[id];if(!e)return;
 if(st.view==="simple"&&e.el.closest&&e.el.closest(".detail")){st.view="detail";save();pendingScroll=id;render();return;}
 if(e.go)e.go();
 e.el.scrollIntoView({block:"start"});
 document.querySelectorAll(".hl").forEach(function(x){x.classList.remove("hl");});
 var n=e.el;n.classList.add("hl");window.clearTimeout(hlTimer);hlTimer=window.setTimeout(function(){n.classList.remove("hl");},1800);
}
function goTo(id){var t=mkLink(st.lang,id);if(location.hash===t)reveal(id);else location.hash=t;}
function setLang(l){
 st.lang=l;save();
 var p=parseToken(location.hash);
 if(p.id){try{history.replaceState(null,"",mkLink(l,p.id));}catch(e){}pendingScroll=p.id;}
 render();
}
function showMsg(text,withInput,val){
 var b=$("linkmsg");b.textContent=text;b.hidden=false;
 if(withInput){var i=document.createElement("input");i.type="text";i.readOnly=true;i.value=val;i.setAttribute("aria-label",text);b.appendChild(i);i.focus();i.select();}
 window.clearTimeout(msgTimer);msgTimer=window.setTimeout(function(){b.hidden=true;},withInput?15000:2500);
}
function linkBtn(id,cls){
 var b=el("button",cls||"lnk","#");b.type="button";
 var lbl=T("Copy link to this section","このセクションへのリンクをコピー");b.title=lbl;b.setAttribute("aria-label",lbl);
 b.addEventListener("click",function(){
  var tok=mkLink(st.lang,id),url=location.href.split("#")[0]+tok;
  if(location.hash!==tok)location.hash=tok;
  var done=T("Copied ","コピーしました ")+tok,fb=T("Copy this link: ","このリンクをコピーしてください: ")+tok;
  try{navigator.clipboard.writeText(url).then(function(){showMsg(done);},function(){showMsg(fb,true,url);});}
  catch(e){showMsg(fb,true,url);}
 });
 return b;
}
var KIND_EN={section:"section",highlight:"highlight",diagram:"diagram item",row:"table row"},KIND_JA={section:"セクション",highlight:"ハイライト",diagram:"図の要素",row:"表の行"};
function buildIndex(){
 $("lidxsum").textContent=T("All link targets","すべてのリンク先")+" ("+REG.list.length+")";
 var ul=$("lidxlist");ul.textContent="";
 REG.list.forEach(function(e){var li=el("li");var a=el("a",null,mkLink(st.lang,e.id));a.href=mkLink(st.lang,e.id);a.addEventListener("click",function(ev){ev.preventDefault();goTo(e.id);});
  li.appendChild(a);li.appendChild(document.createTextNode(" ("+T(KIND_EN[e.kind],KIND_JA[e.kind])+") "+L(e.label)));ul.appendChild(li);});
}

var anims=[];
var keep={};
var viz=window.RakitsuViz.bind({el:el,T:T,L:L,codeCell:codeCell,anims:anims,keep:keep});
var Anim=viz.Anim;var FlowMap=viz.FlowMap;
function frame(now){for(var k=0;k<anims.length;k++)anims[k].step(now);window.requestAnimationFrame(frame);}

/* ---------- widget 1: ReAct loop ---------- */
function reactW(host){
 var fig=el("div","fig");host.appendChild(fig);
 var N={answer:{x:20,y:6,w:120,h:40,label:"Answer",sub:T("no tool call","ツール呼び出しなし")},
  think:{x:20,y:84,w:120,h:56,label:"Think",sub:T("model call","モデル呼び出し")},
  act:{x:260,y:84,w:120,h:56,label:"Act",sub:"cli fs mcp a2a"},
  obs:{x:500,y:84,w:120,h:56,label:"Observe",sub:T("result to history","結果を履歴へ")},
  refl:{x:260,y:178,w:120,h:44,label:"Reflect",sub:T("optional","任意")}};
 var caps=[
  T("Think: the model reads the task and the history so far, then decides what to do next.","Think: モデルがタスクとこれまでの履歴を読み、次の行動を決めます。"),
  T("Act: the model asks for a tool call. Rakitsu runs it (several calls in the same turn run in parallel).","Act: モデルがツール呼び出しを要求します。Rakitsu がそれを実行します (同じターンの複数の呼び出しは並列で実行)。"),
  T("Observe: the tool result is fenced, truncated if long, and appended to the history as new input. Then the loop goes back to Think (it passes through Reflect only if reflection is enabled).","Observe: ツールの結果は囲い込まれ、長ければ切り詰められ、新しい入力として履歴に追加されます。その後ループは Think に戻ります (Reflect を通るのは reflection が有効なときだけです)。"),
  T("Reflect (optional): one extra model call without tools reviews the last action and its result. Off unless reflection is enabled.","Reflect (任意): ツールなしのモデル呼び出しをもう 1 回行い、直前の行動と結果を見直します。reflection を有効にしない限り行いません。"),
  T("Think again: with the new result in the history, the model decides again. If it needs no more tools, it replies without a tool call.","もう一度 Think: 履歴に新しい結果が入った状態で、モデルがもう一度判断します。ツールがこれ以上不要なら、ツール呼び出しなしで返答します。"),
  T("Answer: when the model replies without a tool call, the loop ends and that reply is the result.","Answer: モデルがツール呼び出しなしで返答すると、ループが終わり、その返答が結果になります。")];
 var an=viz.hero(fig,N,caps,T("Animated diagram of the ReAct loop: Think, Act, Observe, with optional Reflect","ReAct ループの図: Think、Act、Observe と任意の Reflect"));
 ["think","act","observe","reflect","answer"].forEach(function(k,i){i=[0,1,2,3,5][i];regItem("react-"+k,{en:"ReAct step: "+k.charAt(0).toUpperCase()+k.slice(1),ja:"ReAct のステップ: "+k.charAt(0).toUpperCase()+k.slice(1)},host,function(){an.goto(i);});});
}

/* ---------- generic clickable flow map ---------- */
function flowW(host){
 var w=132,h=52;function n(x,y,l,s){return {x:x,y:y,w:w,h:h,label:l,sub:s};}
 var info={
  cfg:{what:{en:"One YAML file declares settings, tools, agents and an orchestrator. Viper loads it, and ${ENV_VAR} references resolve from the environment, so keys stay out of the file.",ja:"1 つの YAML ファイルで、設定、ツール、エージェント、オーケストレーターを宣言します。Viper が読み込み、${ENV_VAR} の参照は環境変数から解決されるので、キーはファイルに書かれません。"},keys:["name","settings","tools","agents","orchestrator"],src:"internal/config/config.go"},
  rt:{what:{en:"Builds the system from the config: the LLM providers, the tool registry (global tools: and per-agent tools_inline:), the agents, and the orchestrator. Then it runs it.",ja:"設定から全体を組み立てます。LLM プロバイダ、ツールレジストリ (グローバルの tools: とエージェントごとの tools_inline:)、エージェント、オーケストレーター。そして実行します。"},keys:["tools","tools_inline","settings.memory","settings.spawn"],src:"cmd/rakitsu/run.go, cmd/rakitsu/runtime.go"},
  ag:{what:{en:"The ReAct loop: build the history, call the model, run tool calls, fence the output, optionally reflect, repeat. It ends on an answer, max_iterations, or a budget.",ja:"ReAct ループ。履歴を組み立て、モデルを呼び、ツールを実行し、出力を囲い込み、必要なら reflection を行い、繰り返します。回答、max_iterations、予算のいずれかで終わります。"},keys:["max_iterations","settings.reflection","max_cost","max_total_tokens","fence_outputs"],src:"internal/agent/agent.go"},
  llm:{what:{en:"A provider interface with implementations for OpenAI-compatible endpoints (OpenAI, Ollama, LiteLLM, NVIDIA), Anthropic, Gemini and Codex. Each agent can use a different provider.",ja:"プロバイダのインターフェース。実装は OpenAI 互換エンドポイント (OpenAI、Ollama、LiteLLM、NVIDIA)、Anthropic、Gemini、Codex。エージェントごとに別のプロバイダを使えます。"},keys:["settings.providers.<name>","agent.provider","agent.model","settings.default_provider","--provider","--model"],src:"internal/llm/"},
  tools:{what:{en:"The tool registry: cli (command allowlist or Docker sandbox), fs (path limits), mcp_server and a2a clients. Output is fenced and truncated before the model sees it.",ja:"ツールレジストリ。cli (コマンド許可リストまたは Docker サンドボックス)、fs (パス制限)、mcp_server と a2a のクライアント。モデルが見る前に、出力は囲い込まれ切り詰められます。"},keys:["tools[].type","sandbox.type","allowed_paths","settings.allowed_commands"],limit:{en:"The local_restricted allowlist is a guard rail, not a security boundary. Use docker for untrusted work.",ja:"local_restricted の許可リストはガードレールであり、セキュリティ境界ではありません。信頼できない処理には docker を使ってください。"},src:"internal/tools/"},
  bus:{what:{en:"Every step emits a typed event (agent start and end, tool call, reflection, and more). Consumers: the console tracer, the SSE endpoint, and the hub forwarder.",ja:"各ステップが型付きイベント (エージェントの開始と終了、ツール呼び出し、reflection など) を出します。受け手は、コンソールトレーサ、SSE エンドポイント、ハブへの転送です。"},keys:["--trace","--hub","--no-hub","--debug-port"],src:"internal/telemetry/events.go, internal/telemetry/forwarder.go"},
  hub:{what:{en:"rakitsu serve hosts the hub: run registration, event ingest, SSE broadcast to browsers, and a queue of debug commands. The same process also serves /mcp"+(has("monitors")?", /a2a and /healthz.":" and /a2a.")+"",ja:"rakitsu serve がハブを担います。run の登録、イベントの受け取り、ブラウザへの SSE 配信、デバッグコマンドのキュー。同じプロセスが /mcp、/a2a"+(has("monitors")?"、/healthz":"")+" も提供します。"},keys:["serve --port","serve --host","RAKITSU_API_TOKEN"],limit:{en:"Binds localhost by default. Any other host needs RAKITSU_API_TOKEN.",ja:"既定では localhost にバインドします。それ以外のホストには RAKITSU_API_TOKEN が必要です。"},src:"internal/server/hub.go, internal/server/sse.go"},
  ui:{what:{en:"The embedded Vue app: visual builder, run inspector and debugger. The debugger sends commands (pause, breakpoint, parameter override) back through the hub to the run.",ja:"埋め込みの Vue アプリ。ビジュアルビルダー、実行インスペクタ、デバッガ。デバッガはコマンド (一時停止、ブレークポイント、パラメータ上書き) をハブ経由で run に送り返します。"},keys:[],src:"web/src/, internal/webui/embed.go"}};
 var an=FlowMap(host,{onUser:function(k){userHash(FID[k]);},key:"flow",vb:"0 0 640 310",label:T("Data flow map from YAML config to the web UI","YAML 設定から Web UI までのデータフロー図"),first:"ag", hint:{en:"Click or focus a node to see what it does and which config key controls it. Moving dashes and arrowheads show the direction of flow, not real timing.",ja:"ノードをクリック (またはフォーカス) すると、役割と制御する設定キーを表示します。流れる破線と矢印はデータの向きを示すもので、実際のタイミングではありません。"},
  nodes:{cfg:n(8,16,"YAML config","settings tools agents"),rt:n(172,16,"Runtime",T("builds the system","全体を組み立てる")),ag:n(336,16,"Agent loop","ReAct"),llm:n(500,16,"LLM provider",T("per agent","エージェントごと")),
   tools:n(336,130,"Tools","cli fs mcp a2a"),bus:n(500,130,"Event bus",T("typed events","型付きイベント")),hub:n(336,244,"SSE hub","rakitsu serve"),ui:n(172,244,"Web UI",T("builder, debugger","ビルダー、デバッガ"))},
  edges:[{pts:[[140,42],[172,42]],from:"cfg",to:"rt"},{pts:[[304,42],[336,42]],from:"rt",to:"ag"},{pts:[[468,34],[500,34]],from:"ag",to:"llm"},{pts:[[500,52],[468,52]],from:"llm",to:"ag"},
   {pts:[[380,68],[380,130]],from:"ag",to:"tools"},{pts:[[424,130],[424,68]],from:"tools",to:"ag"},{pts:[[460,68],[530,130]],from:"ag",to:"bus"},
   {pts:[[530,182],[450,244]],from:"bus",to:"hub"},{pts:[[336,262],[304,262]],from:"hub",to:"ui"},{pts:[[304,282],[336,282]],from:"ui",to:"hub",dash:true}],
  info:info});
 var FID={cfg:"flow-config",rt:"flow-runtime",ag:"flow-agent",llm:"flow-llm",tools:"flow-tools",bus:"flow-bus",hub:"flow-hub",ui:"flow-ui"};
 var FJA={cfg:"YAML 設定",rt:"ランタイム",ag:"エージェントループ",llm:"LLM プロバイダ",tools:"ツール",bus:"イベントバス",hub:"SSE ハブ",ui:"Web UI"};
 var FEN={cfg:"YAML config",rt:"Runtime",ag:"Agent loop",llm:"LLM provider",tools:"Tools",bus:"Event bus",hub:"SSE hub",ui:"Web UI"};
 Object.keys(FID).forEach(function(k){regItem(FID[k],{en:"Data flow node: "+FEN[k],ja:"データフローのノード: "+FJA[k]},host,function(){an.select(k);an.pause();});});
}

/* ---------- widget 3: orchestration playground ---------- */
function orchW(host){
 var mode=keep.orchMode||"react",gateCalls=!!keep.orchGate,cur=null;
 var tabs=el("div","tabs");host.appendChild(tabs);
 var opt=el("label","hint");var cb=document.createElement("input");cb.type="checkbox";cb.id="gatecb";opt.appendChild(cb);
 opt.appendChild(document.createTextNode(" "+T("Pipeline demo: the Developer really calls write_file","Pipeline のデモ: Developer が実際に write_file を呼ぶ")));host.appendChild(opt);
 var holder=el("div","fig");host.appendChild(holder);
 var MODES=[["react","ReAct"],["pipeline","Pipeline"],["hier","Supervisor + workers"],["loop","Loop + condition_agent"]];
 MODES.forEach(function(m){var b=el("button",null,m[1]);b.type="button";b.setAttribute("data-m",m[0]);b.onclick=function(){mode=m[0];keep.orchMode=mode;build();userHash("orch-"+m[0]);};tabs.appendChild(b);});
 cb.checked=gateCalls;
 cb.onchange=function(){gateCalls=cb.checked;keep.orchGate=gateCalls;build();};
 function def(){
  var W=function(x,y,w,l,s){return {x:x,y:y,w:w,h:54,label:l,sub:s};};
  if(mode==="react")return {vb:"0 0 640 250",nodes:{sup:W(230,14,180,"Supervisor",T("LLM picks the next step","LLM が次の手を選ぶ")),w1:W(30,170,170,"LogAnalyzer","worker"),w2:W(235,170,170,"InfraEngineer","worker"),w3:W(440,170,170,"APISpecialist","worker")},
   hops:[{a:"sup",b:"w1",t:T("The supervisor calls the tool delegate_to_LogAnalyzer(task).","Supervisor がツール delegate_to_LogAnalyzer(task) を呼びます。")},
    {a:"w1",b:"sup",t:T("LogAnalyzer runs its own ReAct loop and returns a result as a tool result.","LogAnalyzer が自分の ReAct ループを回し、ツール結果として返します。")},
    {a:"sup",b:"w3",t:T("From that result the supervisor chooses the next worker. The order is the model's decision.","その結果を見て、Supervisor が次のワーカーを選びます。順序はモデルの判断です。")},
    {a:"w3",b:"sup",t:T("The result comes back the same way.","結果は同じ経路で戻ります。")},
    {a:"sup",b:"sup",k:"done",t:T("Nothing left to delegate: the supervisor writes the final answer.","委譲することがなくなり、Supervisor が最終回答を書きます。")}],
   note:T("Non-deterministic: which worker runs, and in what order, can differ between runs. Example: examples/single/03-react-team.","非決定的: どのワーカーがどの順で動くかは、実行ごとに変わることがあります。例: examples/single/03-react-team。")};
  if(mode==="hier")return {vb:"0 0 640 250",nodes:{sup:W(200,14,240,"Orchestrator",T("role: supervisor (synthesized)","role: supervisor (自動生成)")),w1:W(30,170,170,"Worker A","agents:"),w2:W(235,170,170,"Worker B","agents:"),w3:W(440,170,170,"Worker C","agents:")},
   hops:[{a:"sup",b:"w1",t:T("The synthesized supervisor delegates to Worker A through a delegation tool.","自動生成された Supervisor が、委譲ツールで Worker A に仕事を渡します。")},
    {a:"w1",b:"sup",t:T("Worker A returns its result.","Worker A が結果を返します。")},
    {a:"sup",b:"w2",t:T("Then Worker B.","次に Worker B です。")},{a:"w2",b:"sup",t:T("Worker B returns its result.","Worker B が結果を返します。")},
    {a:"sup",b:"sup",k:"done",t:T("The supervisor combines the results into one answer.","Supervisor が結果を 1 つの回答にまとめます。")}],
   note:T("Honest note: strategy Hierarchical currently runs through the same ReAct loop, with a synthesized supervisor agent on top. It is not a separate algorithm. PlanAndExecute is equivalent to ReAct today.","正直な注記: strategy: Hierarchical は現在、同じ ReAct ループを通り、その上に自動生成の supervisor エージェントが載るだけです。別のアルゴリズムではありません。PlanAndExecute は現時点で ReAct と同等です。"),warn:true};
  if(mode==="pipeline"){var H=[{a:"pl",b:"dev",t:T("The Planner's output is passed to the Developer as context. Order is fixed in config.","Planner の出力が文脈として Developer に渡ります。順序は設定で固定されています。")},
    {a:"dev",b:"gate",t:T("The Developer answers: 'Done, I wrote the file.' The gate ignores the words and reads the tool-call record instead.","Developer が「完了、ファイルを書きました」と答えます。ゲートは言葉ではなく、ツール呼び出しの記録を読みます。")}];
   if(gateCalls){H.push({a:"gate",b:"rev",t:T("The record shows a write_file call. The step passes and the Reviewer starts.","記録に write_file の呼び出しがあります。ステップは通過し、Reviewer が始まります。")});H.push({a:"rev",b:"rev",k:"done",t:T("The pipeline continues to its end. Each step can also be parallel or a loop.","パイプラインは最後まで進みます。各ステップは parallel や loop にもできます。")});}
   else H.push({a:"gate",b:"gate",k:"fail",t:T("checkRequireToolCall: the record shows 0 write_file calls. The step fails, whatever the agent claimed.","checkRequireToolCall: 記録に write_file の呼び出しは 0 回です。エージェントが何と言おうと、ステップは失敗します。")});
   return {vb:"0 0 640 250",nodes:{pl:W(10,50,110,"Planner","step 1"),dev:W(140,50,110,"Developer","step 2"),gate:W(270,50,190,"gate",'require_tool_call: write_file'),rev:W(490,50,130,"Reviewer","step 3"),log:W(140,160,190,"tool log",gateCalls?"write_file calls: 1":"write_file calls: 0")},hops:H,
   note:T("Deterministic: config fixes the order. require_tool_call is a mechanical cross-check on top. Untick the box above to see the catch.","決定的: 順序は設定で固定されます。require_tool_call はその上に重なる機械的な照合です。上のチェックを外すと、検出される様子が見られます。")};}
  return {vb:"0 0 640 250",nodes:{wk:W(40,80,170,"Worker agent",T("body of the loop step","loop ステップの本体")),jd:W(290,80,180,"Judge","condition_agent"),out:W(530,80,90,"done","")},
   hops:[{a:"wk",b:"jd",t:T("Iteration 1: the Worker's output goes to the condition_agent.","反復 1: Worker の出力が condition_agent に渡ります。")},
    {a:"jd",b:"wk",t:T("The Judge does not answer done, so the loop repeats.","Judge は done と答えないので、ループが繰り返されます。")},
    {a:"wk",b:"jd",t:T("Iteration 2: improved output goes to the Judge again.","反復 2: 改善された出力が再び Judge に渡ります。")},
    {a:"jd",b:"out",t:T("The Judge returns done. The loop ends (or it would stop at max_iterations).","Judge が done を返し、ループが終わります (なければ max_iterations で止まります)。")}],
   note:T("The loop keys are max_iterations, condition_agent and condition_prompt. A wrong Judge can end the loop early or never.","ループのキーは max_iterations、condition_agent、condition_prompt です。Judge の判断が誤ると、早く終わったり終わらなかったりします。")};
 }
 function build(){
  if(cur)cur.kill();holder.textContent="";
  Array.prototype.forEach.call(tabs.children,function(b){b.setAttribute("aria-pressed",String(b.getAttribute("data-m")===mode));});
  opt.hidden=mode!=="pipeline";
  var D=def();
  cur=viz.orchestration(holder,D,T("Orchestration animation: "+mode,"オーケストレーションの動き: "+mode));cur.wid="orch";
 }
 build();
 var NSTEP={react:5,hier:5,loop:4},PNAME=["handoff","require-tool-call","gate-result","done"];
 var MEN={react:"ReAct",pipeline:"Pipeline",hier:"Supervisor + workers",loop:"Loop + condition_agent"};
 MODES.forEach(function(m){
  regItem("orch-"+m[0],{en:"Orchestration tab: "+MEN[m[0]],ja:"オーケストレーションのタブ: "+MEN[m[0]]},host,function(){mode=m[0];keep.orchMode=mode;build();});
  var cnt=m[0]==="pipeline"?4:NSTEP[m[0]];
  for(var q=0;q<cnt;q++)(function(i){
   var id=m[0]==="pipeline"?"pipeline-"+PNAME[i]:"orch-"+m[0]+"-step-"+(i+1);
   regItem(id,{en:"Orchestration step "+(i+1)+" ("+MEN[m[0]]+")",ja:"オーケストレーションの手順 "+(i+1)+" ("+MEN[m[0]]+")"},host,function(){
    mode=m[0];keep.orchMode=mode;if(m[0]==="pipeline"&&i===3){gateCalls=true;keep.orchGate=true;cb.checked=true;}build();if(cur)cur.goto(i);});
  })(q);
 });
}

/* ---------- widget 4: wake timer ---------- */
function wakeW(host){
 var fig=el("div","fig");host.appendChild(fig);
 var OUT=["q","q","q","q","chg","q","q","alarm","sup","q","q","q"];
 var LB={q:T("quiet","静か"),chg:T("changed","変化"),alarm:T("alarm","アラーム"),sup:T("repeat","重複")};
 var DS={q:T("Checks ran and nothing changed. 0 model calls.","チェックを実行しましたが変化なし。モデル呼び出しは 0 回。"),
  chg:T("A check reports a change. The session starts one agent turn (L2): 1 model call.","チェックが変化を報告。セッションがエージェントのターン (L2) を 1 回開始: モデル呼び出し 1 回。"),
  alarm:T("A check raises an alarm. Another agent turn: 1 model call.","チェックがアラームを出しました。もう 1 回のエージェントターン: モデル呼び出し 1 回。"),
  sup:T("The same alarm with no state change is skipped (single_flight). 0 model calls.","状態が変わらない同じアラームはスキップ (single_flight)。モデル呼び出しは 0 回。")};
 var an=viz.wake(fig,OUT,LB,DS,T("Wake timer timeline: many quiet ticks, a few model turns","ウェイクタイマーのタイムライン: 静かなティックが多く、モデルのターンは少数"));
 fig.appendChild(el("div","hint",T("Sample timeline, not a measurement. Real limits: max_turns_per_hour 6, interval 60 s backing off by 1.5x up to 600 s.","サンプルのタイムラインであり、計測値ではありません。実際の上限: max_turns_per_hour 6、間隔は 60 秒から 1.5 倍ずつ延び、最大 600 秒。")));
 OUT.forEach(function(o,i){regItem("wake-tick-"+(i+1),{en:"Wake timeline tick "+(i+1)+" ("+({q:"quiet",chg:"changed",alarm:"alarm",sup:"repeat"})[o]+")",ja:"ウェイクのタイムライン ティック "+(i+1)+" ("+({q:"静か",chg:"変化",alarm:"アラーム",sup:"重複"})[o]+")"},fig,function(){an.goto(i);});});
}

/* ---------- widget 5: memory recall ---------- */
var MEM=[
 {ty:"procedure",ti:"Failed deploy checklist",tx:"Check the logs, confirm which step failed, run the rollback, then notify the channel."},
 {ty:"procedure",ti:"Rollback procedure",tx:"To roll back a deploy, redeploy the previous release tag and check health."},
 {ty:"gotcha",ti:"Cache after deploy",tx:"After a deploy, clear the cache or users see stale pages."},
 {ty:"fact",ti:"Staging host",tx:"Staging runs on a separate host behind the VPN."},
 {ty:"rule",ti:"Commit messages",tx:"Use conventional commits: feat, fix, chore, docs."},
 {ty:"pattern",ti:"Retry with backoff",tx:"Retry failed requests with exponential backoff and jitter."}];
function tok(s){return s.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);}
function bm25(q,docs){var N=docs.length,k1=1.5,b=.75,tk=docs.map(function(d){return tok(d);}),avg=tk.reduce(function(s,d){return s+d.length;},0)/N,qs=[];
 tok(q).forEach(function(w){if(qs.indexOf(w)<0)qs.push(w);});
 return {qs:qs,sc:tk.map(function(d){var s=0;qs.forEach(function(w){var n=0,tf=0;tk.forEach(function(e){if(e.indexOf(w)>-1)n++;});d.forEach(function(x){if(x===w)tf++;});
  if(!tf)return;var idf=Math.log(1+(N-n+.5)/(n+.5));s+=idf*tf*(k1+1)/(tf+k1*(1-b+b*d.length/avg));});return s;})};}
function memW(host){
 var fig=el("div","fig");host.appendChild(fig);
 var qr=el("div","qrow");fig.appendChild(qr);
 var ql=el("label",null,T("Task text","タスクの文"));ql.setAttribute("for","memq");var qi=document.createElement("input");qi.type="text";qi.id="memq";qi.value=keep.memq!=null?keep.memq:"deploy failed rollback";
 var kl=el("label",null,"top_k");kl.setAttribute("for","memk");var ks=document.createElement("select");ks.id="memk";[1,2,3,4,5].forEach(function(n){var o=document.createElement("option");o.value=n;o.textContent=n+(n===5?" ("+T("default","既定")+")":"");ks.appendChild(o);});ks.value="3";
 qr.appendChild(ql);qr.appendChild(qi);qr.appendChild(kl);qr.appendChild(ks);if(keep.memk)ks.value=keep.memk;
 var chips=el("div");fig.appendChild(chips);var rows=el("div","mem");fig.appendChild(rows);
 var pre=el("div","box");var pp=el("pre");pre.appendChild(pp);fig.appendChild(pre);
 var cap=el("p","cap");fig.appendChild(cap);
 fig.appendChild(el("div","hint",T("Sample notes are made up for this page. Scores use the standard BM25 formula with k1=1.5 and b=0.75, so they will differ from a real store, which also adds a small recency boost.","ノートはこのページ用の作り物です。スコアは k1=1.5、b=0.75 の標準的な BM25 の式で計算しており、小さな新しさの加点もある実際のストアとは値が異なります。")));
 var R=null,anim=null;
 function refresh(){var r=bm25(qi.value,MEM.map(function(m){return m.ti+" "+m.tx+" "+m.ty;}));
  var order=MEM.map(function(m,i){return i;}).sort(function(a,b){return r.sc[b]-r.sc[a];});var mx=Math.max.apply(null,r.sc)||1;
  R={r:r,order:order,mx:mx};chips.textContent="";r.qs.forEach(function(w){var hit=MEM.some(function(m){return tok(m.ti+" "+m.tx+" "+m.ty).indexOf(w)>-1;});chips.appendChild(el("span","chip"+(hit?" hit":""),w));});
  rows.textContent="";R.els=order.map(function(i){var d=el("div","mrow");var tt=el("div","tt");tt.appendChild(el("b",null,MEM[i].ti));tt.appendChild(document.createTextNode(" ["+MEM[i].ty+"]"));d.appendChild(tt);
   var br=el("div","bar2");var bi=el("i");br.appendChild(bi);d.appendChild(br);var sc=el("div","sc");d.appendChild(sc);rows.appendChild(d);return {d:d,bi:bi,sc:sc,i:i};});
  if(anim)draw(anim.i,anim.t);}
 var CP=[T("1. The task text is split into terms (highlighted when a note contains them).","1. タスクの文を語に分けます (ノートに含まれる語は強調表示)。"),
  T("2. Each note is scored with BM25 over its title, content and tags.","2. 各ノートのタイトル、本文、タグに対して BM25 でスコアを付けます。"),
  T("3. The best top_k notes are kept. Notes with no matching term are never selected.","3. 上位 top_k 件を残します。一致する語がないノートは選ばれません。"),
  T("4. They are injected as a fenced block at the start of the run, into the model's working history only. The saved session does not contain it.","4. 実行の開始時に、囲み付きのブロックとして、モデルの作業用履歴にだけ注入されます。保存されるセッションには含まれません。")];
 function draw(i,t){if(!R)return;var k=+ks.value;
  R.els.forEach(function(x,rank){var s=R.r.sc[x.i],top=i>=2&&rank<k&&s>0;var w=(i===0?0:i===1?t:1)*(s/R.mx)*100;x.bi.style.width=w+"%";
   x.sc.textContent=(i>=1&&(i>1||t>.4))?s.toFixed(2):"";tog(x.d,"top",top);tog(x.d,"dim",i>=2&&!top);});
  var sel=R.order.slice(0,k).filter(function(j){return R.r.sc[j]>0;});
  var txt=sel.length?"<recalled_memory>\n"+sel.map(function(j){return "- ["+MEM[j].ty+"] "+MEM[j].ti;}).join("\n")+"\n</recalled_memory>":T("(no note matches: nothing is injected)","(一致するノートなし: 何も注入されません)");
  pp.textContent=txt+"\n"+T("(layout simplified)","(書式は簡略化)");pre.style.opacity=i===3?(.35+.65*t):(i>3?1:.35);cap.textContent=CP[i];}
 qi.addEventListener("input",function(){keep.memq=qi.value;refresh();});ks.addEventListener("change",function(){keep.memk=ks.value;refresh();});
 anim=Anim(fig,{n:4,dur:2200,rest:{i:3,t:1},draw:draw});refresh();
}

/* ---------- widget 6: protocol map ---------- */
function protoW(host){
 var w=150,h=54;function n(x,y,l,s){return {x:x,y:y,w:w,h:h,label:l,sub:s};}
 var info={
  acp:{what:{en:"ACP connects an editor to an agent. Run rakitsu acp <config.yaml> and an ACP-speaking editor (such as Zed) can create a session, send prompts and receive streaming updates over stdio JSON-RPC.",ja:"ACP はエディタとエージェントをつなぎます。rakitsu acp <config.yaml> を実行すると、ACP に対応したエディタ (Zed など) が、stdio の JSON-RPC でセッション作成、プロンプト送信、ストリーミング更新の受信を行えます。"},keys:["rakitsu acp <config>"],limit:has("wake")?{en:"The ACP server cannot run wake timers. Use serve for those.",ja:"ACP サーバーではウェイクタイマーを使えません。その場合は serve を使ってください。"}:undefined,src:"internal/acp/server.go"},
  mcpc:{what:{en:"MCP, server side. rakitsu serve exposes its registered tools at POST /mcp so an MCP client can discover and call them.",ja:"MCP のサーバー側。rakitsu serve は登録済みのツールを POST /mcp で公開し、MCP クライアントが検出して呼び出せるようにします。"},keys:["serve --mcp-port N --config <config>","RAKITSU_API_TOKEN"],limit:{en:"Covers the legacy MCP era (revisions through 2025-11-25). The 2026-07-28 revision is not implemented yet.",ja:"レガシー世代の MCP (2025-11-25 までのリビジョン) に対応します。2026-07-28 リビジョンは未実装です。"},src:"internal/server/mcp.go"},
  rk:{what:{en:"The Rakitsu process. It can be an ACP server (stdio), an MCP and A2A server (via serve), and a client of MCP servers and other A2A agents through tools.",ja:"Rakitsu のプロセス。ACP サーバー (stdio)、MCP と A2A のサーバー (serve 経由) になれ、ツールを通じて MCP サーバーや他の A2A エージェントのクライアントにもなります。"},keys:["rakitsu acp","rakitsu serve"],src:"cmd/rakitsu/"},
  mcps:{what:{en:"MCP, client side. A tool of type mcp_server lets an agent call tools from an MCP server, over stdio (a subprocess) or http.",ja:"MCP のクライアント側。type: mcp_server のツールで、エージェントは MCP サーバーのツールを stdio (サブプロセス) または http で呼び出せます。"},keys:["tools[].type: mcp_server","transport","max_response_bytes"],src:"internal/tools/mcp/"},
  peer:{what:{en:"A2A connects agents across processes. The a2a tool delegates to a named agent in another rakitsu process, polling GetTask while the remote task is working. serve --config answers at POST /a2a and publishes an Agent Card at /.well-known/agent-card.json.",ja:"A2A はプロセスをまたいでエージェント同士をつなぎます。a2a ツールは別の rakitsu プロセスの名前付きエージェントに委譲し、リモートのタスクが実行中の間は GetTask をポーリングします。serve --config は POST /a2a に応答し、/.well-known/agent-card.json でエージェントカードを公開します。"},keys:["tools[].type: a2a","url","agent","serve --config <config>"],limit:{en:"No streaming, no push notifications, no Agent Card signing. Tasks are kept in memory and lost on restart.",ja:"ストリーミング、プッシュ通知、エージェントカードの署名には未対応。タスクはメモリ上にあり、再起動で失われます。"},src:"cmd/rakitsu/a2a_serve.go, internal/tools/a2a/tool.go"}};
 var an=FlowMap(host,{onUser:function(k){userHash(PID[k]);},key:"proto",vb:"0 0 640 270",label:T("Protocol map: ACP, MCP and A2A around a Rakitsu process","Rakitsu を中心にした ACP、MCP、A2A のプロトコル図"),first:"acp", hint:{en:"Click or focus a node to see what it does and which config key controls it. Moving dashes and arrowheads show the direction of flow, not real timing.",ja:"ノードをクリック (またはフォーカス) すると、役割と制御する設定キーを表示します。流れる破線と矢印はデータの向きを示すもので、実際のタイミングではありません。"},
  nodes:{acp:n(10,16,T("Editor","エディタ"),"ACP client"),mcpc:n(10,200,"MCP client","POST /mcp"),rk:n(245,108,"Rakitsu",T("agents + tools","エージェント + ツール")),mcps:n(480,16,"MCP server",T("tools","ツール")),peer:n(480,200,T("Other rakitsu","別の rakitsu"),"A2A agent")},
  edges:[{pts:[[160,30],[245,120]],from:"acp",to:"rk"},{pts:[[245,138],[160,50]],from:"rk",to:"acp"},{pts:[[160,214],[245,150]],from:"mcpc",to:"rk"},{pts:[[245,162],[160,230]],from:"rk",to:"mcpc"},
   {pts:[[395,120],[480,30]],from:"rk",to:"mcps"},{pts:[[480,50],[395,138]],from:"mcps",to:"rk"},{pts:[[395,152],[480,214]],from:"rk",to:"peer"},{pts:[[480,236],[395,160]],from:"peer",to:"rk"}],
  info:info});
 var PID={acp:"proto-acp",mcpc:"proto-mcp-client",rk:"proto-rakitsu",mcps:"proto-mcp-server",peer:"proto-a2a-peer"};
 var PEN={acp:"Editor (ACP client)",mcpc:"MCP client",rk:"Rakitsu",mcps:"MCP server",peer:"Other rakitsu (A2A)"};
 var PJA={acp:"エディタ (ACP クライアント)",mcpc:"MCP クライアント",rk:"Rakitsu",mcps:"MCP サーバー",peer:"別の rakitsu (A2A)"};
 Object.keys(PID).forEach(function(k){regItem(PID[k],{en:"Protocol map node: "+PEN[k],ja:"プロトコル図のノード: "+PJA[k]},host,function(){an.select(k);an.pause();});});
 var l=el("div","hint");l.textContent=T("Lines: ACP is editor to agent. MCP is a client calling tools: an agent calling an MCP server, or any MCP client calling Rakitsu at /mcp (the result comes back). A2A is agent to agent.","線の見かた: ACP はエディタからエージェント、MCP はクライアントがツールを呼ぶ関係で、エージェントから MCP サーバーへ、または任意の MCP クライアントから Rakitsu の /mcp へ向かい、結果が戻ります。A2A はエージェント同士です。");host.appendChild(l);
}

/* ---------- guide content ---------- */
var REFS={
 react:{en:"Yao, Zhao, Yu, Du, Shafran, Narasimhan, Cao (2022). ReAct: Synergizing Reasoning and Acting in Language Models. arXiv:2210.03629.",url:"https://arxiv.org/abs/2210.03629"},
 reflexion:{en:"Shinn, Cassano, Berman, Gopinath, Narasimhan, Yao (2023). Reflexion: Language Agents with Verbal Reinforcement Learning. arXiv:2303.11366.",url:"https://arxiv.org/abs/2303.11366"},
 bm25:{en:"Robertson and Zaragoza (2009). The Probabilistic Relevance Framework: BM25 and Beyond. Foundations and Trends in Information Retrieval 3(4), 333-389.",url:"https://nowpublishers.com/article/Details/INR-019"},
 mcp:{en:"Model Context Protocol specification, revision 2025-11-25.",url:"https://modelcontextprotocol.io/specification/2025-11-25"},
 a2a:{en:"Agent2Agent (A2A) Protocol specification.",url:"https://a2a-protocol.org/latest/specification/"},
 acp:{en:"Agent Client Protocol, introduction.",url:"https://agentclientprotocol.com/overview/introduction"}};
function H(en,ja){return {t:"h",en:en,ja:ja};}
function P(en,ja){return {t:"p",en:en,ja:ja};}
function U(items){return {t:"ul",items:items.map(function(x){return {en:x[0],ja:x[1]};})};}
function RL(keys){return {t:"refs",keys:keys};}
var GUIDES=[
{id:"agent-loop",widget:reactW,name:{en:"The agent loop: Think, Act, Observe",ja:"エージェントループ: Think、Act、Observe"},
 simple:{en:"The model thinks, calls a tool, reads the result, and repeats until it can answer.",ja:"モデルは考え、ツールを呼び、結果を読み、答えられるまで繰り返します。"},
 detail:[H("The idea","考え方"),
  P("A language model alone only produces text. ReAct lets it mix reasoning with actions: it writes a short thought, asks for a tool call, receives the result as new input, and thinks again. Each result can change the plan. When the model answers without asking for a tool, the loop ends.","言語モデル単体ではテキストを出すだけです。ReAct では、推論と行動を交互に行います。短い思考を書き、ツール呼び出しを要求し、結果を新しい入力として受け取り、また考えます。結果によって計画が変わることもあります。モデルがツールを要求せずに答えた時点でループは終わります。"),
  H("Research","背景の研究"),
  P("ReAct is from Yao et al. (2022). The paper reports that interleaving reasoning traces with task-specific actions improved results on question answering (HotpotQA), fact verification (Fever) and interactive tasks (ALFWorld, WebShop). The reflection step is a separate idea in the spirit of Reflexion (Shinn et al., 2023), where an agent reflects in words on feedback. Rakitsu's version is simpler: one extra model call without tools, and the reflection stays in the run's history. There is no separate reflection memory.","ReAct は Yao ら (2022) の研究です。論文は、推論の過程とタスク固有の行動を交互に行うことで、質問応答 (HotpotQA)、事実検証 (Fever)、対話的タスク (ALFWorld、WebShop) の結果が改善したと報告しています。reflection のステップは別の考えで、エージェントがフィードバックを言葉で振り返る Reflexion (Shinn ら、2023) に通じます。Rakitsu の実装はもっと単純で、ツールなしのモデル呼び出しを 1 回追加するだけです。reflection はその run の履歴に残り、独立した reflection 用のメモリはありません。"),
  RL(["react","reflexion"]),
  H("In Rakitsu","Rakitsu での実装"),
  U([["Loop: internal/agent/agent.go builds the history, calls the model, runs tool calls in parallel, fences and truncates tool output, appends it, and repeats.","ループ: internal/agent/agent.go が履歴を組み立て、モデルを呼び、ツール呼び出しを並列実行し、出力を囲い込んで切り詰め、履歴に追加して繰り返します。"],
   ["Reflection: settings.reflection with enabled, mode (after_tool, before_answer, both), frequency (always, on_error, every_n) with every_n, and an optional prompt. It runs only when enabled is true.","Reflection: settings.reflection。enabled、mode (after_tool、before_answer、both)、frequency (always、on_error、every_n) と every_n、任意の prompt。enabled が true のときだけ動きます。"],
   ["Stops: a final answer, max_iterations (returns the last response as a partial result), or a budget such as max_cost or max_total_tokens.","停止条件: 最終回答、max_iterations (最後の応答を部分結果として返す)、または max_cost や max_total_tokens などの予算。"],
   ["Every step emits events, so the loop can be watched live in the web UI.","各ステップがイベントを出すので、Web UI でループをリアルタイムに見られます。"]]),
  H("Limits","限界"),
  P("The loop is only as reliable as the model. Weak models can skip tools or delegation; force_delegation exists for that case. With no timeout and no max_iterations, only a budget, --idle-timeout or Ctrl+C stops a run.","ループの信頼性はモデルしだいです。弱いモデルはツールや委譲を飛ばすことがあり、その場合のために force_delegation があります。タイムアウトも max_iterations もない場合、実行を止めるのは予算、--idle-timeout、Ctrl+C だけです。")]},
{id:"data-flow",widget:flowW,name:{en:"Data flow: from YAML to the screen",ja:"データフロー: YAML から画面まで"},
 simple:{en:"One YAML file becomes a runtime. Agents call the model and tools, every step becomes an event, and the web UI shows it live.",ja:"1 つの YAML がランタイムになります。エージェントがモデルとツールを呼び、各ステップはイベントになり、Web UI にリアルタイムで表示されます。"},
 detail:[H("The idea","考え方"),
  P("Rakitsu keeps the description of a system (YAML) apart from the engine that runs it. Because every step is also published as an event, the same run can be traced in a terminal, replayed from a file, or watched and paused in the browser.","Rakitsu は、システムの記述 (YAML) と、それを動かすエンジンを分けています。各ステップはイベントとしても出力されるので、同じ run をターミナルで追い、ファイルから再生し、ブラウザで見たり止めたりできます。"),
  H("Reading the map","図の読み方"),
  U([["In rakitsu run, events are posted to a running hub (on by default when a hub is found; --no-hub turns it off). In rakitsu serve, the runner lives in the same process as the hub.","rakitsu run では、実行中のハブへイベントを送ります (ハブが見つかれば既定で有効。--no-hub で無効)。rakitsu serve では、ランナーがハブと同じプロセスにあります。"],
   ["Debug commands travel the other way: browser, then hub, then the run, which polls for them.","デバッグコマンドは逆向きに進みます。ブラウザ、ハブ、そして、それをポーリングしている run の順です。"],
   ["Sessions are also written to one JSONL file per session, which is the record you can list, resume and fork.","セッションはセッションごとに 1 つの JSONL ファイルにも書かれ、一覧、再開、フォークができます。"]]),
  H("Limits","限界"),
  P("Tool sandboxing is a command allowlist or Docker, and the allowlist is a guard rail, not a security boundary. serve binds localhost unless RAKITSU_API_TOKEN is set.","ツールのサンドボックスはコマンド許可リストまたは Docker で、許可リストはガードレールであってセキュリティ境界ではありません。serve は RAKITSU_API_TOKEN を設定しない限り localhost にバインドします。")]},
{id:"orchestration",widget:orchW,name:{en:"Orchestration playground",ja:"オーケストレーションの実験場"},
 simple:{en:"Pick a strategy and press Play to see how work moves between agents.",ja:"戦略を選んで再生すると、エージェント間で仕事が動く様子を見られます。"},
 detail:[H("The idea","考え方"),
  P("One agent with tools is often not enough. Orchestration decides who works next. Either a supervisor model decides (flexible, varies between runs), or the order is fixed in config (repeatable, fewer surprises).","ツールを持つ 1 つのエージェントでは足りないことがよくあります。オーケストレーションは、次に誰が働くかを決めます。Supervisor モデルが決める方法 (柔軟だが実行ごとに変わる) と、順序を設定で固定する方法 (再現しやすく、意外性が少ない) があります。"),
  H("Research","背景の研究"),
  P("Supervisor with workers and fixed pipelines are common agent patterns, not one paper's invention. Rakitsu's ReAct delegation applies the ReAct loop (Yao et al., 2022) to the supervisor: each worker appears to it as a tool named delegate_to_<Agent>.","Supervisor とワーカー、固定のパイプラインは、1 本の論文の発明ではなく、一般的なエージェントのパターンです。Rakitsu の ReAct 委譲は、ReAct のループ (Yao ら、2022) を supervisor に適用したもので、各ワーカーは delegate_to_<Agent> というツールとして見えます。"),
  RL(["react"]),
  H("In Rakitsu","Rakitsu での実装"),
  {t:"kv",head:["Strategy","Status"],rows:[
   ["ReAct",{en:"Supervisor LLM with delegation tools. Implemented.",ja:"委譲ツールを持つ supervisor LLM。実装済み。"}],
   ["Pipeline",{en:"Config-driven steps: sequential, parallel, loop. Each step's output feeds the next. Implemented.",ja:"設定で決まるステップ (sequential、parallel、loop)。各ステップの出力が次へ渡ります。実装済み。"}],
   ["Hierarchical",{en:"Runs through the ReAct loop with a synthesized supervisor. No separate algorithm yet.",ja:"自動生成の supervisor を載せて ReAct ループを通ります。独自のアルゴリズムはまだありません。"}],
   ["PlanAndExecute",{en:"Currently the same as ReAct.",ja:"現時点では ReAct と同じです。"}]]},
  P("require_tool_call checks the record of tool calls, not the agent's words. If the runner cannot report its tool calls (for example a nested orchestrator), the step fails instead of passing on trust.","require_tool_call は、エージェントの言葉ではなく、ツール呼び出しの記録を確認します。ランナーがツール呼び出しを報告できない場合 (たとえば入れ子のオーケストレーター) は、信用して通すのではなく、ステップを失敗にします。"),
  {t:"code",text:"pipeline:\n  steps:\n    - name: implement\n      agent: Developer\n      require_tool_call:\n        tool: write_file\n    - name: refine\n      type: loop\n      max_iterations: 3\n      condition_agent: Judge\n      steps:\n        - { name: draft, agent: Writer }"},
  H("Limits","限界"),
  P("Besides ReAct, only Pipeline has its own implementation. Separately, settings.spawn gives agents a spawn_agent tool for runtime fan-out (off by default; children cannot spawn further by default).","ReAct のほかに独自の実装があるのは Pipeline だけです。これとは別に、settings.spawn はエージェントに、実行時に並列展開する spawn_agent ツールを与えます (既定は無効。既定では子エージェントはさらに起動できません)。")]},
{id:"wake",needs:"wake",widget:wakeW,name:{en:"Wake timer: check cheaply, think rarely",ja:"ウェイクタイマー: 安く確認し、考えるのはまれに"},
 simple:{en:"A clock runs free checks. The model is called only when something changes. Experimental.",ja:"タイマーが無料のチェックを実行し、変化があったときだけモデルを呼びます。実験的機能です。"},
 detail:[H("The idea","考え方"),
  P("Watching something all day with a model is expensive if the model reads every reading, and most readings are boring. So the work is split: cheap code checks run on a clock, and the model sleeps until a check says something changed.","1 日中モデルに監視させると、毎回の値をモデルが読むので高くつきますが、ほとんどの値は変化のないものです。そこで仕事を分けます。軽いコードのチェックをタイマーで回し、チェックが変化を告げるまでモデルは眠ります。"),
  H("Research","背景の研究"),
  P("No single paper sits behind this. It is a cost-control design: cheap checks first, the expensive model on escalation. Rakitsu names the tiers L0 (free tick), L1 (optional judge, config-only today) and L2 (a full agent turn).","特定の論文に基づくものではありません。コストを抑える設計で、まず安いチェック、必要なときだけ高価なモデルを使います。Rakitsu は階層を L0 (無料ティック)、L1 (任意の judge、現時点では設定のみ)、L2 (完全なエージェントターン) と呼びます。"),
  H("In Rakitsu","Rakitsu での実装"),
  U([["settings.wake.enabled is opt-in. interval_seconds 60, backoff_factor 1.5 after each quiet tick, up to max_interval_seconds 600.","settings.wake.enabled はオプトインです。interval_seconds は 60、静かなティックのたびに backoff_factor 1.5 倍、max_interval_seconds 600 まで。"],
   ["max_turns_per_hour 6 is a hard cap on L2 turns. kill_switch_file stops the loop when the file exists.","max_turns_per_hour 6 は L2 ターンの厳格な上限です。kill_switch_file のファイルが存在するとループが止まります。"],
   ["Check types: file_mtime, file_contains, http_status, http_json. Events: WAKE_TICK, WAKE_ESCALATE, WAKE_TASK_START, WAKE_TASK_END. Quiet ticks emit nothing.","チェックの種類: file_mtime、file_contains、http_status、http_json。イベント: WAKE_TICK、WAKE_ESCALATE、WAKE_TASK_START、WAKE_TASK_END。静かなティックは何も出しません。"],
   ["Worked example: examples/single/14-long-running-monitor. Every key is listed in the Wake timer entry of the reference below.","実例: examples/single/14-long-running-monitor。全キーは、下のリファレンスの「ウェイクタイマー」にあります。"]]),
  H("Limits","限界"),
  P("Experimental. The timer logic is tested with a fake clock, and a multi-day live run is still in progress. Wake is refused when the config has cli, fs, mcp_server or a2a tools. The L1 judge makes no call yet. rakitsu acp cannot run wake timers.","実験的機能です。タイマーのロジックは偽の時計でテストされており、数日間の実運用での検証は進行中です。設定に cli、fs、mcp_server、a2a のツールがあると wake は拒否されます。L1 の judge はまだ呼び出しを行いません。rakitsu acp ではウェイクタイマーは使えません。")]},
{id:"memory",widget:memW,name:{en:"Memory and recall",ja:"メモリとリコール"},
 simple:{en:"Notes are stored as files. At the start of a run, the best few matches are added to the prompt.",ja:"ノートはファイルとして保存されます。実行の開始時に、最も合う数件がプロンプトに追加されます。"},
 detail:[H("The idea","考え方"),
  P("A model forgets everything between runs. Memory gives it notes it can look up. Ranking decides which notes deserve the limited space in the prompt.","モデルは実行のたびにすべてを忘れます。メモリは、参照できるノートを与えます。ランキングは、限られたプロンプトの枠に入れるノートを決めます。"),
  H("Research","背景の研究"),
  P("Ranking uses BM25, a classic keyword-scoring function from information retrieval. It rewards terms that are frequent in a note but rare across all notes, and it discounts long notes. See Robertson and Zaragoza (2009).","ランキングには、情報検索の古典的なキーワードスコア関数 BM25 を使います。1 つのノートに多く、全ノートでは少ない語を高く評価し、長いノートは割り引きます。Robertson と Zaragoza (2009) を参照してください。"),
  RL(["bm25"]),
  H("In Rakitsu","Rakitsu での実装"),
  U([["settings.memory.enabled registers the memory_* tools on every agent. Nodes are JSON files per scope under ~/.rakitsu/memory (the dir key).","settings.memory.enabled で、すべてのエージェントに memory_* ツールが登録されます。ノードはスコープごとの JSON ファイルで、~/.rakitsu/memory (dir キー) に保存されます。"],
   ["Scoring: BM25 over title, content and tags with k1=1.5 and b=0.75, plus a recency boost of at most 10% with a 30-day half-life (internal/memory/bm25.go).","スコア: タイトル、本文、タグに対する BM25 (k1=1.5、b=0.75) に、最大 10%、半減期 30 日の新しさの加点 (internal/memory/bm25.go)。"],
   ["settings.memory.auto_recall: enabled, top_k (default 5), scopes (session, project, global), node_type. The query is the task text.","settings.memory.auto_recall: enabled、top_k (既定 5)、scopes (session、project、global)、node_type。クエリはタスクの文です。"],
   ["The block goes into the working history for each iteration, never into the saved session. Superseded or retired notes are never recalled.","ブロックは各反復の作業用履歴に入り、保存されるセッションには入りません。置き換え済みや廃止済みのノートはリコールされません。"]]),
  H("Limits","限界"),
  P("Matching is by keywords, so a note that says the same thing in different words can be missed. The widget above uses made-up notes and the standard formula, not a real store.","一致はキーワードによるため、同じ内容を別の言葉で書いたノートは見逃されることがあります。上の図は作り物のノートと標準の式を使っており、実際のストアではありません。")]},
{id:"protocols",widget:protoW,name:{en:"Protocols: MCP, ACP, A2A",ja:"プロトコル: MCP、ACP、A2A"},
 simple:{en:"Three open protocols connect Rakitsu to tools, to editors, and to other agents.",ja:"3 つのオープンなプロトコルで、Rakitsu をツール、エディタ、他のエージェントにつなぎます。"},
 detail:[H("The idea","考え方"),
  P("Three different conversations. MCP: an agent talks to tools. ACP: an editor talks to an agent. A2A: one agent talks to another. Using standard protocols means Rakitsu does not need a custom adapter for each partner.","3 種類の会話があります。MCP はエージェントとツール、ACP はエディタとエージェント、A2A はエージェント同士です。標準のプロトコルを使うので、相手ごとに専用のアダプターは要りません。"),
  H("Research","背景の研究"),
  P("MCP is defined by the Model Context Protocol specification (revision 2025-11-25 is the one Rakitsu's server targets). A2A is an open standard for communication between independent agent systems. ACP standardizes communication between code editors and coding agents, with JSON-RPC over stdio for local agents.","MCP は Model Context Protocol の仕様で定義されています (Rakitsu のサーバーが対象とするのは 2025-11-25 リビジョンです)。A2A は、独立したエージェントシステム同士が通信するためのオープンな標準です。ACP は、コードエディタとコーディングエージェントの通信を標準化するもので、ローカルのエージェントでは stdio 上の JSON-RPC を使います。"),
  RL(["mcp","a2a","acp"]),
  H("In Rakitsu","Rakitsu での実装"),
  {t:"kv",head:["Protocol","Where it lives"],rows:[
   ["MCP",{en:"Client: tool type mcp_server (stdio or http), internal/tools/mcp. Server: rakitsu serve --mcp-port N --config exposes POST /mcp, internal/server/mcp.go.",ja:"クライアント: ツール種別 mcp_server (stdio または http)、internal/tools/mcp。サーバー: rakitsu serve --mcp-port N --config が POST /mcp を公開、internal/server/mcp.go。"}],
   ["ACP",{en:"rakitsu acp <config.yaml>, JSON-RPC over stdio, internal/acp.",ja:"rakitsu acp <config.yaml>、stdio 上の JSON-RPC、internal/acp。"}],
   ["A2A",{en:"Client: tool type a2a, internal/tools/a2a. Server: POST /a2a and /.well-known/agent-card.json from serve --config, cmd/rakitsu/a2a_serve.go.",ja:"クライアント: ツール種別 a2a、internal/tools/a2a。サーバー: serve --config による POST /a2a と /.well-known/agent-card.json、cmd/rakitsu/a2a_serve.go。"}]]},
  H("Limits","限界"),
  U([["MCP server: legacy era, revisions through 2025-11-25. The 2026-07-28 revision is not implemented yet.","MCP サーバー: レガシー世代、2025-11-25 までのリビジョン。2026-07-28 リビジョンは未実装です。"],
   ["A2A server: no streaming, no push notifications, no Agent Card signing. Tasks live in memory only. A remote request picks the local agent through the spec's tenant field.","A2A サーバー: ストリーミング、プッシュ通知、エージェントカードの署名はなし。タスクはメモリ上のみ。リモートのリクエストは、仕様の tenant フィールドでローカルのエージェントを選びます。"],
   ["RAKITSU_API_TOKEN gates /mcp and /a2a on the main serve port.","メインの serve ポートでは、RAKITSU_API_TOKEN が /mcp と /a2a を保護します。"]])]}
];
var CASES=[
 ["examples/single/03-react-team",{en:"Incident triage",ja:"障害の切り分け"},{en:"A supervisor delegates to LogAnalyzer, InfraEngineer and APISpecialist to look for the root cause of a latency spike.",ja:"Supervisor が LogAnalyzer、InfraEngineer、APISpecialist に仕事を渡し、遅延の急増の原因を探します。"}],
 ["examples/single/04-pipeline",{en:"Content pipeline",ja:"コンテンツのパイプライン"},{en:"Researcher, Drafter and Editor in order, then three translators (ES, JP, FR) in parallel, then a synthesis step.",ja:"Researcher、Drafter、Editor が順に動き、3 人の翻訳者 (ES、JP、FR) が並列に動き、最後に統合ステップがあります。"}],
 ["examples/single/05-dev-team",{en:"Dev team pipeline",ja:"開発チームのパイプライン"},{en:"Planner, Developer, Verifier and Reviewer in sequence, with file read, file write and command tools.",ja:"Planner、Developer、Verifier、Reviewer が順に動き、ファイルの読み書きとコマンドのツールを使います。"}],
 ["examples/single/10-spawn-fanout",{en:"Parallel research fan-out",ja:"並列リサーチの展開"},{en:"A coordinator splits a task and calls spawn_agent once per part; subagents run in parallel up to max_concurrent.",ja:"Coordinator がタスクを分け、部分ごとに spawn_agent を呼びます。サブエージェントは max_concurrent まで並列に動きます。"}],
 ["examples/single/14-long-running-monitor",{en:"Unattended monitoring",ja:"無人モニタリング"},{en:"Run a persistent monitor without watching every tick. Scheduled checks make zero model calls while quiet; a change or alarm wakes the agent. It can start only allowlisted task configs, with hourly and concurrency caps, a kill switch, health checks, and resume after a serve restart.",ja:"各 tick を人が見張らずに、継続するモニターを実行します。静かな間はスケジュール確認だけでモデル呼び出しはゼロ。変化やアラームでエージェントが起動します。起動できるのは許可リストのタスク設定のみで、毎時と同時実行の上限、停止スイッチ、ヘルスチェック、serve 再起動後の再開に対応します。"},"wake"],
 ["examples/single/09-memory-chat",{en:"Memory chat",ja:"メモリ付きチャット"},{en:"An assistant with native memory tools, a rolling summary of older turns and auto-recall of relevant notes.",ja:"組み込みのメモリツール、古いターンのローリング要約、関連ノートの自動リコールを備えたアシスタント。"}],
 ["examples/single/11-vision-chat",{en:"Vision chat",ja:"画像を扱うチャット"},{en:"An agent with vision: true and --attach for image input. It needs a vision-capable model.",ja:"vision: true のエージェントと、画像入力用の --attach。画像対応のモデルが必要です。"}],
 ["rakitsu serve --config",{en:"Agents as endpoints",ja:"エージェントをエンドポイントとして公開"},{en:"serve exposes your agents to other tools at /mcp and /a2a, next to the web UI.",ja:"serve は、Web UI のほかに、/mcp と /a2a で、あなたのエージェントを他のツールへ公開します。"}]];
var WORKS=[
 {en:"The ReAct loop with parallel tool calls, budgets, optional reflection and ground check.",ja:"並列ツール呼び出し、予算、任意の reflection と ground check を備えた ReAct ループ。"},
 {en:"Pipeline with sequential, parallel and loop steps, and the require_tool_call check.",ja:"sequential、parallel、loop のステップと、require_tool_call による確認を備えた Pipeline。"},
 {en:"Providers: OpenAI, Anthropic, Gemini, a Codex login, and OpenAI-compatible endpoints such as Ollama and LiteLLM.",ja:"プロバイダ: OpenAI、Anthropic、Gemini、Codex のログイン、Ollama や LiteLLM などの OpenAI 互換エンドポイント。"},
 {en:"Web UI: visual builder, run inspector, debugger with breakpoints, session history with replay.",ja:"Web UI: ビジュアルビルダー、実行インスペクタ、ブレークポイント付きデバッガ、リプレイ付きセッション履歴。"},
 {en:"Sessions saved as JSONL, with resume and fork.",ja:"JSONL で保存されるセッション。再開とフォークに対応。"},
 {en:"MCP client and server, A2A client and server, ACP server, within the limits listed above.",ja:"MCP のクライアントとサーバー、A2A のクライアントとサーバー、ACP サーバー。上に挙げた制限の範囲で。"}];
var ROUGH=[
 {en:"Pre-release software. Interfaces and behavior may change without notice.",ja:"プレリリース版のソフトウェアです。インターフェースや動作は予告なく変わることがあります。"},
 {en:"Hierarchical and PlanAndExecute have no separate implementation yet.",ja:"Hierarchical と PlanAndExecute には、まだ独自の実装がありません。"},
 {en:"The local_restricted command allowlist is a guard rail, not a security boundary. Use Docker for untrusted work.",ja:"local_restricted のコマンド許可リストはガードレールであり、セキュリティ境界ではありません。信頼できない処理には Docker を使ってください。"},
 {en:"Wake timer and monitors are experimental. A multi-day live run is still in progress.",ja:"ウェイクタイマーとモニターは実験的機能です。数日間の実運用での検証は進行中です。"},
 {en:"The MCP server covers revisions through 2025-11-25 only. The A2A server has no streaming and keeps tasks in memory.",ja:"MCP サーバーは 2025-11-25 までのリビジョンのみです。A2A サーバーはストリーミングがなく、タスクをメモリ上に置きます。"},
 {en:"Results depend on the model. Weak models can skip tools or delegation.",ja:"結果はモデルに左右されます。弱いモデルはツールや委譲を飛ばすことがあります。"},
 {en:"Docs exist only for releases whose docs changed. A release with no doc changes shows the docs of the nearest earlier release, and the version list says which.",ja:"ドキュメントがあるのは、内容が変わったリリースだけです。ドキュメントの変更がないリリースは、直前の古いリリースのドキュメントを表示し、バージョン一覧にその旨を表示します。"}];

/* ---------- rendering ---------- */
function mediaBlock(m){
 if(!m)return null;var d=m.diagram,s=m.screenshot,v=m.video;if(!(d&&d.src)&&!(s&&s.src)&&!(v&&v.src))return null;
 var box=el("div","media");
 [d,s].forEach(function(x){if(x&&x.src){var i=document.createElement("img");i.src=x.src;i.alt=L(x.alt)||"";i.loading="lazy";box.appendChild(i);}});
 if(v&&v.src){var vi=document.createElement("video");vi.src=v.src;if(v.poster)vi.poster=v.poster;vi.controls=true;vi.preload="none";box.appendChild(vi);}
 return box;}
var ctx={feat:null,tbl:0,name:null};
var ROWSCOPE={cli:["","run-","serve-","","acp-","healthcheck-"]};
var ROUGH_NEEDS={"wake-monitors":"wake"};
var WORKS_IDS=["react-loop","pipeline","providers","web-ui","sessions","protocols"],ROUGH_IDS=["alpha","hierarchical","allowlist","wake-monitors","mcp-a2a-limits","model-dependent","docs-coverage"];
function renderBlock(b){
 if(b.t==="p")return el("p",null,L(b));
 if(b.t==="h")return el("h3",null,L(b));
 if(b.t==="ul"){var u=el("ul");b.items.forEach(function(i){u.appendChild(el("li",null,L(i)));});return u;}
 if(b.t==="code"){var bx=el("div","box");bx.appendChild(el("pre",null,b.text));return bx;}
 if(b.t==="refs"){var r=el("p","hint");r.appendChild(document.createTextNode(T("Sources: ","出典: ")));b.keys.forEach(function(k,i){var a=el("a",null,k);a.href=REFS[k].url;a.target="_blank";a.rel="noopener";if(i)r.appendChild(document.createTextNode(", "));r.appendChild(a);});return r;}
 if(b.t==="kv"){
  var bx2=el("div","box"),t=el("table"),th=el("thead"),tr=el("tr");
  b.head.forEach(function(h){tr.appendChild(el("th",null,st.lang==="ja"&&HEADJA[h]?HEADJA[h]:h));});
  th.appendChild(tr);t.appendChild(th);var tb=el("tbody");ctx.tbl++;
  b.rows.forEach(function(r){var row=el("tr");var rid=null;
   if(ctx.feat&&typeof r[0]==="string"){var key=rowKey(r[0]),sc=(ROWSCOPE[ctx.feat]||[])[ctx.tbl-1]||"",lb=lab(ctx.name,": "+r[0],": "+r[0]);
    [ctx.feat+"-"+sc+key,ctx.feat+"-t"+ctx.tbl+"-"+sc+key].some(function(c){return regDom(c,"row",lb,row)&&(rid=c);});}
   r.forEach(function(c,i){var td=el("td");
   if(typeof c==="string"){if(c===""){td.textContent="";}else if(i===0||i<r.length-1){td.appendChild(codeCell(c));}else td.textContent=c;}
   else td.textContent=L(c);if(i===0&&rid)td.appendChild(linkBtn(rid,"lnk lnk-row"));row.appendChild(td);});tb.appendChild(row);});
  t.appendChild(tb);bx2.appendChild(t);return bx2;}
 return el("span");
}
function section(id,name,simple,media,domId){var s=el("section","feat");var did=domId||id;var ok=regDom(did,"section",lab(name),s);var h=el("h2",null,L(name));h.appendChild(codeCell(id));if(ok)h.appendChild(linkBtn(did));s.appendChild(h);if(simple){var one=el("p","one",L(simple));if(regDom(did+"-summary","highlight",lab(name," (summary)"," (要約)"),one))one.appendChild(linkBtn(did+"-summary"));s.appendChild(one);}var m=mediaBlock(media);if(m)s.appendChild(m);return s;}
function footer(ver){
 var f=FOOT;f.textContent="";f.classList.remove("hl");
 var lh=el("h2",null,T("License","ライセンス"));lh.style.fontSize="16px";lh.style.margin="0 0 6px";if(regDom("license","section",{en:"License",ja:"ライセンス"},f))lh.appendChild(linkBtn("license"));f.appendChild(lh);
 var p1=el("div");p1.appendChild(document.createTextNode("(c) 2026 Sakaki Natan & Paulus Ery Wasito Adhi. All rights reserved."));f.appendChild(p1);
 f.appendChild(el("div",null,T("Use of the software is governed by the Business Source License 1.1.","本ソフトウェアの利用は Business Source License 1.1 に従います。")));
 var p3=el("div");p3.appendChild(document.createTextNode(T("Release ","リリース ")+ver+": "));
 ["LICENSE","NOTICE"].forEach(function(n,i){var a=el("a",null,n);a.href=REPO+"/blob/"+ver+"/"+n;a.target="_blank";a.rel="noopener";if(i)p3.appendChild(document.createTextNode(", "));p3.appendChild(a);});f.appendChild(p3);
 f.appendChild(el("div",null,T("Page assets: IBM Plex Sans and IBM Plex Mono under the SIL Open Font License 1.1, self-hosted (no third-party requests). Japanese text uses the fonts installed on your device. No JavaScript libraries; diagrams are inline SVG. Rakitsu's own third-party components and licenses are listed in the NOTICE file of the release.","ページの素材: IBM Plex Sans、IBM Plex Mono。いずれも SIL Open Font License 1.1 で、このサイトから配信します(外部への通信はありません)。日本語の表示には端末にインストールされているフォントを使います。JavaScript ライブラリは使っていません。図はインライン SVG です。Rakitsu 自体のサードパーティ部品とそのライセンスは、リリースの NOTICE ファイルに記載されています。")));
 f.appendChild(el("div",null,T("Rakitsu is pre-release software. Interfaces and behavior may change without notice.","Rakitsu はプレリリース版のソフトウェアです。インターフェースや動作は予告なく変わることがあります。")));
}
function checkGate(){
 var old=$("gatenote");if(old)old.parentNode.removeChild(old);
 var p=parseToken(location.hash);if(p.id||!p.raw)return;
 var need=needOf(p.raw);if(!need||has(need))return;
 var tag=null;VERSIONS.forEach(function(v){if(!tag&&DATA[v].features.some(function(f){return f.id===need;}))tag=v;});
 if(DEFAULT_VER&&DATA[DEFAULT_VER]&&DATA[DEFAULT_VER].features.some(function(f){return f.id===need;}))tag=DEFAULT_VER;
 var u=UI[st.lang],n=el("div","notice",u.gate.replace("%TAG%",st.ver));n.id="gatenote";n.setAttribute("role","status");
 if(tag){n.appendChild(document.createTextNode(" "));var a=el("a",null,u.gateGo.replace("%TAG%",tag));
  a.href=window.RK_TAG?"../"+encodeURIComponent(tag)+"/"+location.hash:location.hash;
  a.addEventListener("click",function(e){if(window.RK_TAG)return;e.preventDefault();st.ver=tag;save();sel.value=tag;pendingScroll=p.raw;render();});
  n.appendChild(a);}
 else n.appendChild(document.createTextNode(" "+u.gateNone));
 var nt=$("notice");nt.parentNode.insertBefore(n,nt);
}
function render(){
 var saved={};anims.forEach(function(a){if(a.wid)saved[a.wid]={i:a.i,t:a.t,playing:a.playing};});anims.slice().forEach(function(a){a.kill();});anims.length=0;REG.list.length=0;REG.map={};ctx.feat=null;
 var d=data(st.ver),u=UI[st.lang];
 document.documentElement.lang=st.lang;
 document.body.setAttribute("data-view",st.view);
 document.querySelectorAll("[data-i]").forEach(function(n){n.textContent=u[n.getAttribute("data-i")];});
 $("viewSimple").textContent=u.simple;$("viewDetail").textContent=u.detail;
 $("viewSimple").setAttribute("aria-pressed",st.view==="simple");$("viewDetail").setAttribute("aria-pressed",st.view==="detail");
 $("langEn").setAttribute("aria-pressed",st.lang==="en");$("langJa").setAttribute("aria-pressed",st.lang==="ja");
 $("verlabel").textContent=verText(st.ver);fillSel();
 var m=$("meta");m.textContent="";
 [verText(st.ver),(d.meta.license||""),(d.meta.go||""),L(d.meta.status)||""].forEach(function(x){if(x)m.appendChild(el("span",null,x));});
 var n=$("notice");var cov=Object.prototype.hasOwnProperty.call(COVERS,st.ver);n.hidden=!cov;n.textContent=cov?u.notice.replace("%DOC%",docOf(st.ver)):"";
 var nav=$("nav");nav.textContent="";var g=$("guides");g.textContent="";var f=$("feats");f.textContent="";
 function link(id,txt){var a=el("a",null,txt);a.href=mkLink(st.lang,id);a.addEventListener("click",function(e){e.preventDefault();goTo(id);});nav.appendChild(a);}
 nav.appendChild(el("div","grp",u.learn));
 GUIDES.forEach(function(x){if(!has(x.needs))return;link(x.id,L(x.name));var s=section(x.id,x.name,x.simple,x.media);s.classList.add("guide");
  var fig=el("div");s.appendChild(fig);var nb=anims.length;x.widget(fig);for(var q=nb;q<anims.length;q++)anims[q].wid=anims[q].wid||x.id;
  var dt=el("div","detail");x.detail.forEach(function(b){dt.appendChild(renderBlock(b));});s.appendChild(dt);g.appendChild(s);});
 anims.forEach(function(a){var s=saved[a.wid];if(s&&a.restore)a.restore(s);});
 link("can-do",T("What it can do","できること"));
 var cs=section("can-do",{en:"What it can really do",ja:"実際にできること"},{en:"Every card is an example shipped in the repository. None of them is a claim of production use.",ja:"どのカードもリポジトリに同梱の例です。本番利用を示すものではありません。"});
 var cg=el("div","cards");CASES.forEach(function(c){if(!has(c[3]))return;var cd=el("div","card");var ch=el("h3",null,L(c[1]));var cid="card-"+(slug(c[0].split("/").pop().replace(/^\d+-/,"").replace(/^rakitsu\s+/,""))||"x");if(regDom(cid,"highlight",c[1],cd))ch.appendChild(linkBtn(cid));cd.appendChild(ch);cd.appendChild(el("p",null,L(c[2])));cd.appendChild(sampleLink(c[0]));cg.appendChild(cd);});cs.appendChild(cg);g.appendChild(cs);
 link("works-today",T("Works today, still rough","動くもの、まだ粗いもの"));
 var ws=section("works-today",{en:"Works today, and what is still rough",ja:"今動くものと、まだ粗いもの"},{en:"Rakitsu is pre-release software and is not production-ready.",ja:"Rakitsu はプレリリース版のソフトウェアで、本番利用の準備はできていません。"});
 var tw=el("div","two");var a1=el("div");a1.appendChild(el("h3","okh",T("Works today","今動くもの")));var l1=el("ul");WORKS.forEach(function(x,i){var li=el("li",null,L(x));var wid="works-"+WORKS_IDS[i];if(regDom(wid,"highlight",x,li))li.appendChild(linkBtn(wid));l1.appendChild(li);});a1.appendChild(l1);
 var a2=el("div");a2.appendChild(el("h3","badh",T("Still rough","まだ粗いもの")));var l2=el("ul");ROUGH.forEach(function(x,i){if(!has(ROUGH_NEEDS[ROUGH_IDS[i]]))return;var li=el("li",null,L(x));var rid2="rough-"+ROUGH_IDS[i];if(regDom(rid2,"highlight",x,li))li.appendChild(linkBtn(rid2));l2.appendChild(li);});a2.appendChild(l2);
 tw.appendChild(a1);tw.appendChild(a2);ws.appendChild(tw);g.appendChild(ws);
 $("refhead").textContent=u.refhead;
 nav.appendChild(el("div","grp",T("Reference","リファレンス")));
 if(!d.features.length){f.appendChild(el("p","hint",u.noref));}
 d.features.forEach(function(ft){link("ref-"+ft.id,L(ft.name));
  var s=section(ft.id,ft.name,ft.simple,ft.media,"ref-"+ft.id);s.querySelector("h2 code").textContent=ft.id;
  ctx.feat=ft.id;ctx.tbl=0;ctx.name=ft.name;var dt=el("div","detail");ft.detail.forEach(function(b){dt.appendChild(renderBlock(b));});s.appendChild(dt);f.appendChild(s);});
 link("references",u.refs);
 ctx.feat=null;var rs=$("refsbox");rs.textContent="";var rh=el("h2",null,u.refs);if(regDom("references","section",{en:"References",ja:"参考文献"},$("references")))rh.appendChild(linkBtn("references"));rs.appendChild(rh);
 var rl=el("div","refs");Object.keys(REFS).forEach(function(k){var p=el("div");p.appendChild(document.createTextNode(REFS[k].en+" "));var a=el("a",null,REFS[k].url);a.href=REFS[k].url;a.target="_blank";a.rel="noopener";p.appendChild(a);rl.appendChild(p);});
 rs.appendChild(rl);rs.appendChild(el("p","hint",T("Each entry was checked against its source page on 2026-10-03.","各項目は 2026-10-03 に出典のページで確認しました。")));
 link("license",T("License","ライセンス"));
 footer(st.ver);buildIndex();checkGate();
 if(pendingScroll!=null){var praw=pendingScroll;pendingScroll=null;window.setTimeout(function(){var c=canon(praw);if(c)reveal(c);},0);}
}
var sel=$("verSel");
function verText(v){if(!Object.prototype.hasOwnProperty.call(COVERS,v))return v;return v+" ("+T("docs from ","ドキュメント: ")+docOf(v)+")";}
function fillSel(){sel.textContent="";TAGS.forEach(function(v){var o=document.createElement("option");o.value=v;o.textContent=verText(v);sel.appendChild(o);});sel.value=st.ver;}
sel.setAttribute("autocomplete","off");
function syncSel(){if(st.ver&&sel.value!==st.ver)sel.value=st.ver;}
window.addEventListener("pageshow",syncSel);
sel.addEventListener("change",function(){if(window.RK_TAG){location.href="../"+encodeURIComponent(sel.value)+"/"+location.hash;return;}st.ver=sel.value;save();render();});
$("viewSimple").addEventListener("click",function(){st.view="simple";save();render();});
$("viewDetail").addEventListener("click",function(){st.view="detail";save();render();});
$("langEn").addEventListener("click",function(){setLang("en");});
$("langJa").addEventListener("click",function(){setLang("ja");});
window.addEventListener("hashchange",function(){var p=parseToken(location.hash);if(p.lang&&p.lang!==st.lang){st.lang=p.lang;save();pendingScroll=p.raw;render();}else{if(p.id)reveal(p.id);checkGate();}});
window.addEventListener("load",function(){var p=parseToken(location.hash);if(p.id)reveal(p.id);});
var p0=parseToken(location.hash);if(p0.lang){st.lang=p0.lang;try{localStorage.setItem("rk-spec",JSON.stringify(st));}catch(e){}}
pendingScroll=p0.raw;
var UIERR={en:"Could not load the spec data (data/versions.json). Browsers block fetch() on file:// pages. Start the local server with: python3 -m http.server 8765 -d site , then open http://127.0.0.1:8765/",
 ja:"仕様データ (data/versions.json) を読み込めませんでした。file:// で開くと、ブラウザが fetch() を止めます。次のコマンドでローカルサーバーを起動し、http://127.0.0.1:8765/ を開いてください: python3 -m http.server 8765 -d site"};
function showLoadError(e){var n=$("loaderr");n.hidden=false;n.textContent=UIERR[st.lang]||UIERR.en;if(window.console)console.error(e);}
var BASE=window.RK_TAG?"../":"";
function getJSON(u){return fetch(u,{cache:"no-cache"}).then(function(r){if(!r.ok)throw new Error(u+" "+r.status);return r.json();});}
getJSON(BASE+"data/versions.json").then(function(vj){
 VERSIONS=vj.versions.slice();DEFAULT_VER=vj.default;COVERS=vj.covers||{};TAGS=VERSIONS.concat(Object.keys(COVERS)).sort(tagCmp);
 return Promise.all(VERSIONS.map(function(v){return getJSON(BASE+"data/"+v+".json").then(function(d){DATA[v]=d;});}));
}).then(function(){
 st.ver=window.RK_TAG&&TAGS.indexOf(window.RK_TAG)>-1?window.RK_TAG:(saved0&&TAGS.indexOf(saved0)>-1?saved0:DEFAULT_VER);
 fillSel();
 render();window.requestAnimationFrame(frame);
 if(typeof window.__rkReady==="function")window.__rkReady();
 window.__rkLoaded=true;
}).catch(showLoadError);
})();
