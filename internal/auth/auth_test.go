package auth

import (
	"net/http"
	"reflect"
	"testing"
)

// hash 值钉死：它是**落库格式**，改算法等于让所有已发出去的 key 一起失效。
// 换算法时这条会红，提醒你那不是一次普通重构。
func TestHashIsPlainSHA256Hex(t *testing.T) {
	if got := Hash("sk-aig-example"); len(got) != 64 {
		t.Fatalf("hash 长度 = %d, 期望 64 位十六进制", len(got))
	}
	// 空串的 SHA-256 是公开常量，用它验算法本身而不是验我们自己算的结果。
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Hash(""); got != emptySHA256 {
		t.Errorf("Hash(\"\") = %q, 期望 %q——不是裸 SHA-256 了", got, emptySHA256)
	}
	if Hash("a") == Hash("b") {
		t.Error("不同输入撞了同一个 hash")
	}
}

func TestPresented(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header map[string]string
		want   []string
	}{
		{"什么都没带", nil, nil},
		{"x-api-key", map[string]string{"x-api-key": "k1"}, []string{"k1"}},
		{"Bearer", map[string]string{"Authorization": "Bearer k1"}, []string{"k1"}},
		{"bearer 小写", map[string]string{"Authorization": "bearer k1"}, []string{"k1"}},
		{"BEARER 大写", map[string]string{"Authorization": "BEARER k1"}, []string{"k1"}},
		// 两个头一起发是常态，顺序固定：x-api-key 在前。
		{"两个都带", map[string]string{"x-api-key": "k1", "Authorization": "Bearer k2"}, []string{"k1", "k2"}},
		{"前后空白", map[string]string{"x-api-key": "  k1  "}, []string{"k1"}},
		{"空串不算出示", map[string]string{"x-api-key": "", "Authorization": ""}, nil},
		// 只有 Bearer 这一种 scheme：Basic 里放的是别的东西，当 key 试没有意义。
		{"Basic 不认", map[string]string{"Authorization": "Basic bGVhaw=="}, nil},
		{"光一个 Bearer 没值", map[string]string{"Authorization": "Bearer "}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.header {
				h.Set(k, v)
			}
			if got := Presented(h); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Presented() = %v, 期望 %v", got, tc.want)
			}
		})
	}
}
