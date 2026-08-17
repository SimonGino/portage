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

// ImageCarrier 是一张图实际填了哪一组来源字段。
//
// 定成具名类型而不是裸 string：这个值在三个 codec 的六个半边上被 switch 十来次，其中
// 两处是「是不是 file」的单点比较——写成裸字面量时打错一个字母照样编译得过，结果是
// image_file_id 这条丢弃记录**静默不再登记**，而「丢得有声音」正是口径层 v0.39 给
// file_id 单开一档的全部理由。让编译器替我们盯着。
type ImageCarrier string

const (
	// CarrierNone：三组来源都是空的，当没有图。
	CarrierNone ImageCarrier = ""
	// CarrierData：裸 base64 + media_type，跨协议真做转换。
	CarrierData ImageCarrier = "data"
	// CarrierURL：远程 URL，原样转发，网关不代下载。
	CarrierURL ImageCarrier = "url"
	// CarrierFile：上游作用域句柄，跨协议登记 image_file_id 后丢。
	CarrierFile ImageCarrier = "file"
)

// Carrier 返回填了哪一组来源。
func (img *Image) Carrier() ImageCarrier {
	if img == nil {
		return CarrierNone
	}
	if !IsEmptyBase64(img.Data) {
		return CarrierData
	}
	if strings.TrimSpace(img.URL) != "" {
		return CarrierURL
	}
	if strings.TrimSpace(img.FileID) != "" {
		return CarrierFile
	}
	return CarrierNone
}

// HasConvertibleImage 判一串块里有没有搬得走的图（data 或 url）。
//
// 放在 protocol 而不是各 codec 各写一份：两个出口半边此前是**逐字节相同**的两个私有
// 函数，而这个判断只认 protocol.Block 与 Carrier()，与出口协议无关。它决定的是「这条
// 消息的 content 要不要从字符串升格成 part 数组」——两边答得不一样就是一边发数组一边
// 发字符串，是那种只在某个上游身上现形的分叉。
func HasConvertibleImage(blocks []Block) bool {
	for _, b := range blocks {
		if b.Kind != BlockImage {
			continue
		}
		switch b.Image.Carrier() {
		case CarrierData, CarrierURL:
			return true
		}
	}
	return false
}
