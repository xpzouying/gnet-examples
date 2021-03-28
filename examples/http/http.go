// Copyright 2019 Andy Pan. All rights reserved.
// Copyright 2017 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/panjf2000/gnet"
)

var res string

// request 定义了http请求中的metadata数据
type request struct {
	proto, method string
	path, query   string
	head, body    string
	remoteAddr    string
}

// 在这里使用了 内嵌结构体的形式。
// *gnet.EventServer 是在gnet库中的默认结构体定义，实现了所有的 EventHandler interface接口约定，但是所有的函数都是空。
// 我们在使用时，不需要让httpServer全部重新实现一遍EventHandler接口中的定义，只需要根据我们自己的需求重写对应的接口。
type httpServer struct {
	*gnet.EventServer
}

var (
	errMsg      = "Internal Server Error"
	errMsgBytes = []byte(errMsg)
)

// TODO(zy): 查看gnet中，怎么把httpServer和httpCodec串起来了
type httpCodec struct {
	req request
}

func (hc *httpCodec) Encode(c gnet.Conn, buf []byte) (out []byte, err error) {
	if c.Context() == nil {
		return buf, nil
	}
	return appendResp(out, "500 Error", "", errMsg+"\n"), nil
}

func (hc *httpCodec) Decode(c gnet.Conn) (out []byte, err error) {
	buf := c.Read()  // 读出所有的http请求数据
	c.ResetBuffer()

	// process the pipeline
	var leftover []byte
pipeline:
	leftover, err = parseReq(buf, &hc.req)
	// bad thing happened - 错误的http请求
	if err != nil {
		c.SetContext(err)
		return nil, err
	} else if len(leftover) == len(buf) {
		// request not ready, yet
		return
	}
	out = appendHandle(out, res)  // 正确的响应返回给请求
	buf = leftover
	goto pipeline
}

// 在httpServer的定义中，我们重写了2个EventHandler中的接口。

// OnInitComplete - 这个接口的重写只是为了输出启动后的日志。
// TODO(zy): gnet中在什么时候调用到该函数。
// TODO(zy): gnet.Action 是什么；怎么用这个Action？
func (hs *httpServer) OnInitComplete(srv gnet.Server) (action gnet.Action) {
	log.Printf("HTTP server is listening on %s (multi-cores: %t, loops: %d)\n",
		srv.Addr.String(), srv.Multicore, srv.NumEventLoop)
	return
}

// React - 关键函数。Reactor模式的关键所在。
func (hs *httpServer) React(frame []byte, c gnet.Conn) (out []byte, action gnet.Action) {
	if c.Context() != nil {
		// bad thing happened
		out = errMsgBytes
		action = gnet.Close
		return
	}

	// TODO(zy): 这里仅仅是透传？需要搞懂处理流程是怎么样的。
	// 把接收到的frame数据，直接传给下个（业务层）处理。
	// handle the request
	out = frame
	return
}

func main() {
	var port int
	var multicore bool

	// Example command: go run http.go --port 8080 --multicore=true
	flag.IntVar(&port, "port", 8080, "server port")
	flag.BoolVar(&multicore, "multicore", true, "multicore")
	flag.Parse()

	res = "Hello World!\r\n"

	// 启动http service的必要条件：
	// 1。http server - 用于监听端口，接收请求的连接。
	// 2。http请求的处理 - 解析请求、处理响应。gnet源码中给的解释为：encodes and decodes TCP stream
	http := new(httpServer)
	hc := new(httpCodec)

	// Start serving!
	log.Fatal(gnet.Serve(http, fmt.Sprintf("tcp://:%d", port), gnet.WithMulticore(multicore), gnet.WithCodec(hc)))
}

// 这里是返回响应。
// appendHandle handles the incoming request and appends the response to
// the provided bytes, which is then returned to the caller.
func appendHandle(b []byte, res string) []byte {
	return appendResp(b, "200 OK", "", res)
}

// 拼接http response
//
// appendResp will append a valid http response to the provide bytes.
// The status param should be the code plus text such as "200 OK".
// The head parameter should be a series of lines ending with "\r\n" or empty.
func appendResp(b []byte, status, head, body string) []byte {
	b = append(b, "HTTP/1.1"...)
	b = append(b, ' ')
	b = append(b, status...)
	b = append(b, '\r', '\n')
	b = append(b, "Server: gnet\r\n"...)
	b = append(b, "Date: "...)
	b = time.Now().AppendFormat(b, "Mon, 02 Jan 2006 15:04:05 GMT")
	b = append(b, '\r', '\n')
	if len(body) > 0 {
		b = append(b, "Content-Length: "...)
		b = strconv.AppendInt(b, int64(len(body)), 10)
		b = append(b, '\r', '\n')
	}
	b = append(b, head...)
	b = append(b, '\r', '\n')
	if len(body) > 0 {
		b = append(b, body...)
	}
	return b
}

// 高效地将 []byte 转换为 string。
// 直接获取 []byte 的地址，然后使用 *string类型 去指向该地址，最后使用 * 操作符获取其中的string类型数据。
func b2s(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// parseReq is a very simple http request parser. This operation
// waits for the entire payload to be buffered before returning a
// valid request.
//
// data是请求的实际数据；req是需要填充获取的、构建出来的http request。
func parseReq(data []byte, req *request) (leftover []byte, err error) {
	sdata := b2s(data)
	var i, s int
	var head string
	var clen int
	q := -1
	// method, path, proto line
	// 在这里对http请求的第一行数据进行解析。可以获取 “请求行”：
	//
	// |请求方法|空格|URL|空格|协议版本|回车符|换行符|
	for ; i < len(sdata); i++ {

		// HTTP协议。每个字段之间都是以空格隔开；
		if sdata[i] == ' ' {
			// 遇到的第一个空格之前的字符串，标记着http method。
			req.method = sdata[s:i]

			for i, s = i+1, i+1; i < len(sdata); i++ {
				if sdata[i] == '?' && q == -1 {
					q = i - s
				} else if sdata[i] == ' ' {
					if q != -1 {
						req.path = sdata[s:q]
						req.query = req.path[q+1 : i]
					} else {
						req.path = sdata[s:i]
					}

					for i, s = i+1, i+1; i < len(sdata); i++ {
						if sdata[i] == '\n' && sdata[i-1] == '\r' {
							req.proto = sdata[s:i]
							i, s = i+1, i+1
							break
						}
					}
					break
				}
			}
			break
		}
	}
	if req.proto == "" {
		return data, fmt.Errorf("malformed request")
	}

	// 解析head的部分
	head = sdata[:s]
	for ; i < len(sdata); i++ {
		if i > 1 && sdata[i] == '\n' && sdata[i-1] == '\r' {
			line := sdata[s : i-1]
			s = i + 1
			if line == "" {
				req.head = sdata[len(head)+2 : i+1]
				i++
				if clen > 0 {
					if len(sdata[i:]) < clen {
						break
					}
					req.body = sdata[i : i+clen]
					i += clen
				}
				return data[i:], nil
			}
			if strings.HasPrefix(line, "Content-Length:") {
				n, err := strconv.ParseInt(strings.TrimSpace(line[len("Content-Length:"):]), 10, 64)
				if err == nil {
					clen = int(n)
				}
			}
		}
	}

	// not enough data
	return data, nil
}
