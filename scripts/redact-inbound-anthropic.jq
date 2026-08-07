# 脱敏 Anthropic 入站样本：**保结构、换文本**。
#
#   jq -f scripts/redact-inbound-anthropic.jq raw/xxx/request.json > golden/xxx/request.json
#
# 为什么不是手工改：脱敏动作本身会改字节，改错了不看发现不了，而这五个样本各 185 KB、
# 42 个 tool 定义——手工过一遍既不可复现也不可复核。写成过滤器，改了什么一目了然。
#
# 保下来的是 codec 真正要测的东西：块数与顺序、cache_control 断点位置、tool 的 name 与
# input_schema 形状、tool_use.id ↔ tool_result.tool_use_id 的配对、消息角色序列。
# 换掉的是长文本本身——codec 不解释提示词内容，替换不掉测试价值。
#
# 换掉的东西分两类：
#   指纹（必须换）—— metadata.user_id 里的 device_id/session_id、system[0] 那条
#     x-anthropic-billing-header（内含 cc_version 与 entrypoint）、thinking.signature。
#   Claude Code 的提示词原文（按 PO 2026-08-07 裁定换）—— system 正文、tool 描述、
#     消息里的 <system-reminder> 块。
#
# 占位符带上原文长度，是为了让「这是个 29 KB 的缓存块」这件事跟着样本走。

def redact(kind): "[redacted \(kind) len=\(length)]";

# scrub_paths 是兜底的一遍：把主机绝对路径换成中性值，不管它藏在哪一层。按项脱敏追不上
# 模型能把路径写到哪儿——它会自己在工具入参里写出 cwd。与 Responses 那份保持同一实现。
def scrub_paths:
  walk(if type == "string" then
         gsub("/private/tmp/claude-[0-9]+/[^\" ]*"; "/tmp/goldenrec-work")
         | gsub("/Users/[^\"/ ]+"; "/Users/tester")
       else . end);

# 每一步都先判类型再改。这不是防御性冗余：jq 的 `.a |= f` 在 `a` 不存在时会**建出**
# 一个 `"a": null`，无条件赋值同理会凭空造出归零字段——往字节保真语料里掺伪造。
# 换一版 harness（system 是纯串、没有 metadata、没有 tools）时，这些守卫决定的是
# 「原样留着」而不是「造一个」。

# 1. 客户端指纹：结构与真实值同形（仍是一串 JSON），值全部归零。
(if (.metadata | type) == "object" and (.metadata | has("user_id")) then
   .metadata.user_id = ({
     device_id: "0000000000000000000000000000000000000000000000000000000000000000",
     account_uuid: "",
     session_id: "00000000-0000-0000-0000-000000000000"
   } | tojson)
 else . end)

# 2. system：块数、type、cache_control 位置一个不动，只换 text。
#    system 也可以是纯字符串（API 允许），那种形态整串换掉。
| (if (.system | type) == "array" then .system |= [.[] | .text |= redact("system-block")]
   elif (.system | type) == "string" then .system |= redact("system-string")
   else . end)

# 3. tools：name 与 input_schema 结构保留，描述文本全换。
#    input_schema 里 properties.description 是个**属性名**叫 description 的对象，
#    下面的 type=="string" 判断刚好把它和真正的描述串分开，别改成按键名匹配。
| (if (.tools | type) == "array" then .tools |= [.[]
     | .description |= redact("tool-desc")
     | .input_schema |= walk(
         if type == "object" and (.description | type) == "string"
         then .description |= redact("schema-desc")
         else . end)]
   else . end)

# 4. messages：只换文本与签名，角色序列、工具 id 配对、tool_result 正文全留
#    （tool_result 里是采集时现造的 alpha-one/beta-three，本就是无意义测试文本）。
| (if (.messages | type) == "array" then .messages |= [.[]
     | if (.content | type) == "string" then
         .content |= redact("mid-conversation-system")
       else
         .content |= [.[]
           | if .type == "thinking" then .signature |= redact("thinking-sig")
             elif .type == "text" and (.text | test("<system-reminder>")) then
               .text |= redact("system-reminder")
             else . end]
       end]
   else . end)

# 5. 兜底扫一遍路径（理由见 scrub_paths）。
| scrub_paths
