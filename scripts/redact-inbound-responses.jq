# 脱敏 Responses（Codex CLI）入站样本：**保结构、换文本**，口径同 Anthropic 那份。
#
#   jq -f scripts/redact-inbound-responses.jq raw/xxx/request.json > golden/xxx/request.json
#
# 保下来的是 `R→CC` 真正要面对的东西：`input` 数组的项序与类型、`additional_tools` 那层
# （这版 Codex 把工具声明成输入项而不是顶层 tools 字段）、`custom_tool_call.call_id` ↔
# `custom_tool_call_output.call_id` 的配对、reasoning 项的位置，以及 `store` /
# `include` / `reasoning.context` 这几个决定语义的顶层字段。
#
# **`reasoning.encrypted_content` 原样保留。** 它是这批样本的存在理由——CC 那边没有任何
# 位置能放它，`R→CC` 必须当面回答「丢还是留」，而只有真的长度与真的不透明性摆在那儿，
# 这个问题才提得出来。内容是模型对「读一个 /tmp 测试文件」的推理密文，不含任何个人信息。
#
# 换掉的：
#   指纹（必须换）—— client_metadata 里的 installation_id / session_id / thread_id /
#     turn_id / window_id（#10 点名的那个 installation_id 就在这里，不在请求头里），
#     prompt_cache_key，以及 <environment_context> 里的 cwd 绝对路径。
#   Codex 的提示词原文 —— developer 消息正文与三个工具的 description。
#
# **input 项的 `id`（`msg_…` / `cmp_…`）留着，是权衡后的决定**（2026-08-13，采压缩样本时定）。
# 它们是 UUIDv7，只带采集当天的时间戳，不含设备、账号或路径——而采集日期本来就写在
# `meta.json` 的 `source` 里。留着换来的是两样东西：`cmp_…` 是压缩样本的证据链本身
# （见下方 input 那节），`msg_…` 则显示同一条 user 消息**跨压缩沿用同一个 id**，
# 全归零成一个值会把这层关系一起抹掉。

def redact(kind): "[redacted \(kind) len=\(length)]";

# scrub_paths 是兜底的一遍：把主机绝对路径换成中性值，不管它藏在哪一层。
#
# 单独一遍而不是在各项里逐个处理，是因为路径会出现在**模型生成的内容**里——这次就有一条：
# 模型自己在 exec 的 JS 入参里写了 `workdir:"/private/tmp/claude-.../scratchpad/work"`，
# 而那串路径里带着用户名与会话 id。按项脱敏永远追不上模型能把路径写到哪儿。
def scrub_paths:
  walk(if type == "string" then
         gsub("/private/tmp/claude-[0-9]+/[^\" ]*"; "/tmp/goldenrec-work")
         | gsub("/Users/[^\"/ ]+"; "/Users/tester")
       else . end);

def zeroed_ids:
  { installation_id: "00000000-0000-0000-0000-000000000000",
    session_id:      "00000000-0000-0000-0000-000000000000",
    thread_id:       "00000000-0000-0000-0000-000000000000",
    turn_id:         "00000000-0000-0000-0000-000000000000",
    window_id:       "00000000-0000-0000-0000-000000000000:0" };

# 每一步都先判类型再改，理由同 Anthropic 那份：jq 的 `.a |= f` 在 `a` 不存在时会
# **建出**一个 `"a": null`，无条件赋值会凭空造出归零字段。Codex 版本一换，
# client_metadata 里有哪几个键是会变的，守卫决定的是「没有就别造」。
def zero_if_present(k; v): if has(k) then .[k] = v else . end;

# 逐项的 `internal_chat_message_metadata_passthrough.turn_id`（Codex 0.147 起有，采
# compaction 样本时才撞见）。它与 client_metadata 里那个 turn_id 是同一个值，漏在这一
# 层等于白洗上面那遍。守卫同 zero_if_present：没有就别造。
def zero_passthrough:
  if (.internal_chat_message_metadata_passthrough | type) == "object"
  then .internal_chat_message_metadata_passthrough |= zero_if_present("turn_id"; zeroed_ids.turn_id)
  else . end;

# 1. 会话与安装指纹。x-codex-turn-metadata 是一串 JSON，保持它仍是一串 JSON。
(if has("prompt_cache_key") then .prompt_cache_key = zeroed_ids.session_id else . end)
| (if (.client_metadata | type) == "object" then .client_metadata |= (
    zero_if_present("x-codex-installation-id"; zeroed_ids.installation_id)
    | zero_if_present("x-codex-window-id"; zeroed_ids.window_id)
    | zero_if_present("session_id"; zeroed_ids.session_id)
    | zero_if_present("turn_id";    zeroed_ids.turn_id)
    | zero_if_present("thread_id";  zeroed_ids.thread_id)
    | if (."x-codex-turn-metadata" | type) == "string" then
        ."x-codex-turn-metadata" |= (
          (fromjson? // {})
          | zero_if_present("installation_id"; zeroed_ids.installation_id)
          | zero_if_present("session_id"; zeroed_ids.session_id)
          | zero_if_present("thread_id";  zeroed_ids.thread_id)
          | zero_if_present("turn_id";    zeroed_ids.turn_id)
          | zero_if_present("window_id";  zeroed_ids.window_id)
          | tojson)
      else . end)
   else . end)

# 2. input：逐项按类型处理，项序与类型一个不动。
#    Responses 的 input 也可以是纯字符串（API 允许），那种形态不含指纹、原样留着。
#
#    `compaction` / `compaction_trigger` 走不到任何分支，**原样留着是有意的**：
#    `compaction.encrypted_content` 与它的 `cmp_…` id 是压缩样本的存在理由——上游那份
#    真密文、以及它与同一批 `response.output_item.done` 的对应关系，正是「客户端取的是
#    done 那份」这个结论的全部证据。理由同 `reasoning.encrypted_content`（见文件头）。
| (if (.input | type) == "array" then .input |= [.[]
    | zero_passthrough
    | if .type == "additional_tools" then
        .tools |= [.[] | .description |= redact("tool-desc")]
      elif .type == "message" then
        # developer 消息是 Codex 的提示词；user 消息里只有 <environment_context> 那条
        # 带 cwd 绝对路径，真正的测试提示词留着——它本就是采集时现造的无意义文本。
        .content |= [.[]
          | if .type == "input_text" and (.text | test("<environment_context>"))
            then .text |= redact("environment-context")
            else . end]
        | if .role == "developer"
          then .content |= [.[] | .text |= redact("codex-instructions")]
          else . end
      else . end]
   else . end)

# 3. 兜底扫一遍路径（理由见 scrub_paths）。
| scrub_paths
