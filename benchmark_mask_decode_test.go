// Copyright 2021-2024 antlabs. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package quickws

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/antlabs/wsutil/enum"
	"github.com/antlabs/wsutil/frame"
	"github.com/antlabs/wsutil/opcode"
)

// ============================================================================
// mask 路径的端到端基准
//
// 为什么单独写一个文件: 现有的 Benchmark_WriteMessage 用的是零值 Conn,
// client=false, 走的是"服务端不 mask"的分支, 完全覆盖不到 mask.Mask()。
// 而 RFC6455 规定客户端发送的帧必须 mask, 也就是说:
//
//	客户端每次 WriteMessage 都要付出一次 mask 的代价
//	服务端每次收到客户端帧都要付出一次 unmask 的代价(方向相反, 同一份代码)
//
// 所以 mask 是真正的热路径, 这里的基准就是量化它的开销。
//
// 运行方式(需要 go.mod 里 replace 指向本地 wsutil 才测得到改动):
//
//	go test -bench Benchmark_ClientMask -benchmem -count=10 . > new.txt
//	benchstat old.txt new.txt
// ============================================================================

// 构造一个 client=true 的 Conn, 后续 WriteMessage 会走 mask 分支
func newClientConnForBench() (*Conn, *bytes.Buffer) {
	buf := bytes.NewBuffer(make([]byte, 0, 64*1024))
	c := &Conn{}
	c.c = &testConn{buf: buf}
	c.client = true
	c.Callback = &DefCallback{}
	c.Config = &Config{}
	return c, buf
}

// Benchmark_ClientMask_WriteMessage 量化客户端写消息时的 mask 开销。
//
// 对比方式: 同一个二进制里跑 client/server 两条路径, 差值就是 mask 的成本。
// 因为两次运行的其他代码路径(编码头部、写固定缓冲)完全一致。
func Benchmark_ClientMask_WriteMessage(b *testing.B) {
	sizes := []int{64, 256, 1024, 4096, 16384}

	for _, sz := range sizes {
		payload := make([]byte, sz)
		for i := range payload {
			payload[i] = byte(i)
		}

		b.Run(benchSizeName(sz)+"/client(mask)", func(b *testing.B) {
			c, buf := newClientConnForBench()
			b.SetBytes(int64(sz))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.WriteMessage(opcode.Binary, payload); err != nil {
					b.Fatal(err)
				}
				buf.Reset()
			}
		})

		b.Run(benchSizeName(sz)+"/server(no-mask)", func(b *testing.B) {
			c, buf := newClientConnForBench()
			c.client = false
			b.SetBytes(int64(sz))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.WriteMessage(opcode.Binary, payload); err != nil {
					b.Fatal(err)
				}
				buf.Reset()
			}
		})
	}
}

// Benchmark_ClientMask_RoundTrip 测"mask 编码 + unmask 解码"的完整往返,
// 更贴近一个真实客户端连接上发生的事。
func Benchmark_ClientMask_RoundTrip(b *testing.B) {
	sizes := []int{256, 1024, 4096, 16384}

	for _, sz := range sizes {
		payload := make([]byte, sz)
		for i := range payload {
			payload[i] = byte(i * 7)
		}

		b.Run(benchSizeName(sz), func(b *testing.B) {
			// 客户端把消息编码成带 mask 的线上字节
			wire := encodeMaskedFrameForBench(b, payload)

			// 每次迭代都从 wire 重新解一帧(含 unmask)
			var head [enum.MaxFrameHeaderSize]byte
			var windows []byte

			b.SetBytes(int64(sz))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := bytes.NewReader(wire)
				f, err := frame.ReadFrameFromReaderV3(r, nil, &head, &windows)
				if err != nil {
					b.Fatal(err)
				}
				if f.Opcode != opcode.Binary {
					b.Fatalf("opcode 不对: %v", f.Opcode)
				}
			}
		})
	}
}

// 用真实客户端路径编码一帧, 拿到线上的字节(含 mask)
func encodeMaskedFrameForBench(b *testing.B, payload []byte) []byte {
	b.Helper()
	buf := bytes.NewBuffer(make([]byte, 0, len(payload)+enum.MaxFrameHeaderSize))
	c, _ := newClientConnForBench()
	c.c = &testConn{buf: buf}
	if err := c.WriteMessage(opcode.Binary, payload); err != nil {
		b.Fatal(err)
	}
	return append([]byte(nil), buf.Bytes()...)
}

func benchSizeName(n int) string {
	switch n {
	case 64:
		return "00064B"
	case 256:
		return "00256B"
	case 1024:
		return "01KB"
	case 4096:
		return "04KB"
	case 16384:
		return "16KB"
	default:
		return itoaBench(n) + "B"
	}
}

func itoaBench(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

var _ = rand.Uint32
