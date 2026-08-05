package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RewriteModel replaces the root object's "model" value with upstreamModel and
// returns the result, leaving every other byte of body untouched.
//
// 接入点对外模型名 → 纳管模型名的翻译（口径层 §2.3）必须发生在字节层：整体
// decode→encode 会打乱键序、改写数字字面量、丢掉未建模的厂商字段，正是「透传保真
// 优先」要避免的。这里只定位 model 值的字节区间做splice，其余字节原样。
//
// 已经等于 upstreamModel、或根本没有顶层 model 键时，原样返回 body。
// 顶层键重复时改写最后一个，与 encoding/json 的 last-wins 语义一致。
func RewriteModel(body []byte, upstreamModel string) ([]byte, error) {
	start, end, err := locateModelValue(body)
	if err != nil {
		return nil, err
	}
	if start < 0 {
		return body, nil
	}

	encoded, err := encodeJSONString(upstreamModel)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(body[start:end], encoded) {
		return body, nil
	}

	out := make([]byte, 0, len(body)-(end-start)+len(encoded))
	out = append(out, body[:start]...)
	out = append(out, encoded...)
	out = append(out, body[end:]...)
	return out, nil
}

// locateModelValue returns the byte span of the root object's "model" string
// value, or start = -1 when there is no such key.
func locateModelValue(body []byte) (start, end int, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, fmt.Errorf("解析请求体: %w", err)
	}
	if tok != json.Delim('{') {
		return -1, 0, nil
	}

	start = -1
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, fmt.Errorf("解析请求体键: %w", err)
		}
		key, _ := keyTok.(string)

		// Decode 整段吞掉值，因此嵌套对象里的同名键不会被误当成顶层键。
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, 0, fmt.Errorf("解析请求体值: %w", err)
		}
		valEnd := int(dec.InputOffset())

		if key != "model" || len(raw) == 0 || raw[0] != '"' {
			continue
		}
		valStart := valEnd - len(raw)
		// 防御：RawMessage 理论上就是源字节，对不上就放弃改写而不是乱切。
		if valStart < 0 || !bytes.Equal(body[valStart:valEnd], raw) {
			continue
		}
		start, end = valStart, valEnd
	}
	return start, end, nil
}

// encodeJSONString quotes s as JSON without escaping HTML, so a model name
// survives round-tripping unchanged.
func encodeJSONString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
