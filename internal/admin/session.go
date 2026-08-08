package admin

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessionTTL 是一次登录的有效期。每次带着有效 cookie 的请求都会把它往后推，
// 所以「一直在用就不会掉线，合上电脑过夜就要重登」。
const sessionTTL = 12 * time.Hour

// sessionStore 是内存里的会话表。
//
// 内存而不是落库（口径层 §2.7）：重启即全部失效，这对单管理员的自用网关是**特性**
// 不是缺陷——密码万一泄露，重启一次就把所有已发出的会话一起吊销了。代价是重启后要
// 重登一次，一个人用无所谓。
type sessionStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: map[string]time.Time{}}
}

// create 发一个新会话，返回它的 token。
//
// 32 字节 crypto/rand：token 是唯一的凭据，必须猜不出来。rand.Read 在现代 Go 上
// 不会失败，真失败了也只能是熵源坏了——那时宁可让登录报错，也不能退化成弱随机。
func (s *sessionStore) create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.m[token] = time.Now().Add(sessionTTL)
	return token, nil
}

// valid 判断 token 是否有效，顺带续期。
func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.m, token)
		return false
	}
	s.m[token] = time.Now().Add(sessionTTL)
	return true
}

func (s *sessionStore) drop(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// dropAll 吊销全部会话。改密码之后调它：不然改密码只挡住了「还没登录的人」，
// 已经拿着 cookie 的那个浏览器照旧能用——而改密码的常见动机恰恰是「怕别人还在用」。
func (s *sessionStore) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = map[string]time.Time{}
}

// sweepLocked 清掉过期条目。挂在 create 上而不是起一个定时器：会话表最多几条，
// 为它常驻一个 goroutine 不值当；而只在 valid 里删的话，登录后再没访问过的死条目
// 会一直留着。
func (s *sessionStore) sweepLocked() {
	now := time.Now()
	for token, exp := range s.m {
		if now.After(exp) {
			delete(s.m, token)
		}
	}
}
