// SPDX-License-Identifier: AGPL-3.0-only
/*
监听地址：只接受数字形式的本机地址，让 HTTP 服务始终处于可信入口之后。
配置校验调用 loopback；例如 loopback("127.0.0.1:8090") 返回 true。
这里不解析主机名，也不创建监听端口；真正的监听由 server.go 负责。
*/
package main

import (
	"net"     // 区分本机地址与可能暴露到外部的地址
	"strconv" // 检查端口是否落在无符号 16 位范围
)

// loopback 判断显式监听地址是否为本机 IP；端口 0 保留给临时端口测试。
func loopback(address string) bool {
	host, port, err := net.SplitHostPort(address) // IPv4、IPv6 都必须明确提供端口
	if err != nil || port == "" {
		return false // 格式错误时不尝试猜测默认端口
	}
	if _, err = strconv.ParseUint(port, 10, 16); err != nil {
		return false // 不接受服务名、负数或超过 65535 的端口
	}
	ip := net.ParseIP(host)             // 不经 DNS，避免名字解析改变监听范围
	return ip != nil && ip.IsLoopback() // 只允许明确的回环 IP
}
