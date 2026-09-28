package protocol_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
)

// TestDocumentBlocksDropAsDocument（#100）：三协议的文件块（A document / CC file /
// R input_file）解码成 BlockDocument，跨协议两个出口登记 document 档而不是
// vendor_content，载荷不外带。工具结果里的文件块单独再跑一遍——那条路此前在 A→R
// 是静默丢，顶层那块登记了会把它盖住。
func TestDocumentBlocksDropAsDocument(t *testing.T) {
	fixtures := []struct {
		name          string
		in            protocol.Protocol
		toolResultDoc bool // 样本在工具结果里也嵌了文件块
	}{
		{"in-anthropic-document", protocol.Anthropic, true},
		{"in-cc-file", protocol.OpenAI, false},
		{"in-responses-input-file", protocol.OpenAIResponses, true},
	}
	outs := []protocol.Protocol{protocol.Anthropic, protocol.OpenAI, protocol.OpenAIResponses}
	for _, fx := range fixtures {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, fx.name, "request.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range outs {
			if out == fx.in {
				continue
			}
			t.Run(fx.name+"→"+string(out), func(t *testing.T) {
				req, err := codecs.New(fx.in, codecs.Options{}).DecodeRequest(raw, false)
				if err != nil {
					t.Fatal(err)
				}
				assertDocumentDropped(t, out, req)

				// 剥掉顶层文件块，只留工具结果里的那块再编一遍。
				if !stripTopLevelDocuments(req) {
					t.Fatal("解码后顶层没有 BlockDocument")
				}
				if hasToolResultDocument(req) != fx.toolResultDoc {
					t.Fatalf("工具结果里有无 BlockDocument = %v，want %v", !fx.toolResultDoc, fx.toolResultDoc)
				}
				if fx.toolResultDoc {
					assertDocumentDropped(t, out, req)
				}
			})
		}
	}
}

func assertDocumentDropped(t *testing.T, out protocol.Protocol, req *protocol.Request) {
	t.Helper()
	enc := codecs.New(out, codecs.Options{DefaultMaxTokens: 1024}).(protocol.RequestEncodeReporter)
	body, dropped, err := enc.EncodeRequestReport(req, false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, d := range dropped {
		kinds[d.Kind] = true
	}
	if !kinds["document"] {
		t.Errorf("dropped = %v, want 含 document", dropped)
	}
	if kinds["vendor_content"] {
		t.Errorf("dropped = %v：文件块是三协议都认得的语义，不该记 vendor_content", dropped)
	}
	for _, leak := range []string{"JVBERi0x", ".pdf"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("请求体里出现了 %q：文件块跨协议只登记后丢", leak)
		}
	}
}

// stripTopLevelDocuments 剥掉消息顶层的文件块，报告剥没剥到。
func stripTopLevelDocuments(req *protocol.Request) bool {
	found := false
	for i := range req.Messages {
		kept := req.Messages[i].Content[:0]
		for _, b := range req.Messages[i].Content {
			if b.Kind == protocol.BlockDocument {
				found = true
				continue
			}
			kept = append(kept, b)
		}
		req.Messages[i].Content = kept
	}
	return found
}

func hasToolResultDocument(req *protocol.Request) bool {
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind != protocol.BlockToolResult || b.ToolResult == nil {
				continue
			}
			for _, inner := range b.ToolResult.Content {
				if inner.Kind == protocol.BlockDocument {
					return true
				}
			}
		}
	}
	return false
}
