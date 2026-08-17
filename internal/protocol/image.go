package protocol

import "strings"

// DefaultImageMediaType 是 media_type 为空时的唯一有损兜底。
const DefaultImageMediaType = "image/png"

// ParseDataURI 拆 data URI。非 data: 串返回 ok=false，调用方把整串当 URL。
// 载荷为空或只剩空白时也返回 ok=false，调用方按「没有图」跳过。
func ParseDataURI(s string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", "", false
	}
	rest := s[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	data = rest[comma+1:]
	if IsEmptyBase64(data) {
		return "", "", false
	}
	meta := rest[:comma]
	mediaType = meta
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		mediaType = meta[:i]
	}
	return mediaType, data, true
}

// FormatDataURI 拼 data URI。MediaType 为空时兜底 image/png。
func FormatDataURI(mediaType, data string) string {
	return "data:" + ImageMediaType(mediaType) + ";base64," + data
}

// IsEmptyBase64 判定裸 base64 载荷是否为空（含只剩空白）。
func IsEmptyBase64(data string) bool {
	return strings.TrimSpace(data) == ""
}

// ImageMediaType 空值兜底为 image/png——媒体类型往返的唯一有损点。
func ImageMediaType(mt string) string {
	if strings.TrimSpace(mt) == "" {
		return DefaultImageMediaType
	}
	return mt
}

// Carrier 返回填了哪一组来源：data / url / file / ""。
func (img *Image) Carrier() string {
	if img == nil {
		return ""
	}
	if !IsEmptyBase64(img.Data) {
		return "data"
	}
	if strings.TrimSpace(img.URL) != "" {
		return "url"
	}
	if strings.TrimSpace(img.FileID) != "" {
		return "file"
	}
	return ""
}
