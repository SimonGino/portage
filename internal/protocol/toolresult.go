package protocol

// MissingToolResultPlaceholder 是「有调用、没结果」时出口侧合成的占位结果正文。
//
// 不变量：assistant 发出的每个工具调用，紧随其后必须有一条配对的结果。DeepSeek V4
// 严格校验这一条，缺了就 400 `Messages with role 'tool' must be a response to a
// preceding message with 'tool_calls'`；Anthropic 同样要求每个 tool_use 在**紧接着
// 的 user 消息里**有对应 tool_result，否则也是 400。而客户端历史确实会缺——取消的
// 轮次、丢掉的 output、Codex 的 bug——上游会拒，客户端又改不了自己的历史，会话就此
// 砖死。合成一条占位是唯一能把这轮救回来的做法。
//
// 文案**明说结果缺失**，不伪装成一次成功或失败的执行（PO 裁定）：模型读到这句会知道
// 这一路没拿到东西，而不是把一段编造的语义当成真的工具输出。
//
// 住在 protocol 而不是两个出口包各写一份：Anthropic 与 CC 的占位必须逐字相同，散开
// 就会漂——漂了之后两条路径的模型行为不一样，而没有任何测试会发现。
//
// 出处：mimo2codex `src/translate/reqToChat.ts` 的 ensureToolCallsHaveOutputs。
const MissingToolResultPlaceholder = "[tool result missing: the client sent no output for this call]"
