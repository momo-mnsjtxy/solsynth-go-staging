// SPDX-License-Identifier: AGPL-3.0-only
/*
数据库连接配置：只采用命令行指定的环境变量里的 PostgreSQL URL。
配置校验先调用 ambientPostgres，再调用 explicitDSN；例如：

	connection, err := explicitDSN("postgres://synthetic@localhost/disposable?sslmode=disable")

这里只整理连接选项，不连接数据库。连接串含密码，调用方不得把返回值写进日志。
*/
package main

import (
	"errors"  // 配置错误不附带密码或完整连接串
	"net"     // 明文连接仅能指向本机 IP
	"net/url" // 解读显式 PostgreSQL URL
	"os"      // 检查会影响 pgx 的环境变量，禁用隐式凭据文件
	"strconv" // 检查端口和超时的数值范围
	"strings" // 检查主机、库名和本机套接字路径
)

// ambientPostgres 拒绝 libpq 环境配置，防止 shell 悄悄覆盖显式连接目标。
func ambientPostgres() error {
	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING", "PGREQUIREAUTH"} {
		if value := os.Getenv(name); value != "" { // 非空值可能改变连接目标或凭据来源
			return errors.New("ambient PostgreSQL configuration is unsupported; use the explicit DSN environment variable")
		}
	}
	return nil // 后续连接仅由显式 URL 决定
}

// explicitDSN 校验数据库边界输入，补上固定的 TLS、超时和凭据来源策略。
// 输入可以是 postgres 或 postgresql URL；返回值保留密码，必须仅传给 sql.Open。
func explicitDSN(raw string) (string, error) {
	connection, err := url.Parse(raw) // 先解读 URL，再检查目标与凭据是否明确
	if err != nil || (connection.Scheme != "postgres" && connection.Scheme != "postgresql") || connection.User == nil || connection.User.Username() == "" || connection.Hostname() == "" || strings.ContainsAny(connection.Host, ", \t\r\n") || strings.Trim(connection.Path, "/") == "" || connection.Fragment != "" || connection.Opaque != "" {
		return "", errors.New("explicit PostgreSQL URL with user, host and database required")
	}
	if port := connection.Port(); port != "" {
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 { // 数据库端口不能为临时端口 0
			return "", errors.New("invalid database port")
		}
	}
	options, err := url.ParseQuery(connection.RawQuery) // 拒绝有歧义或重复的外部选项
	if err != nil {
		return "", errors.New("invalid database URL options")
	}
	for key, values := range options {
		if len(values) != 1 || (key != "sslmode" && key != "connect_timeout" && key != "host") {
			return "", errors.New("unsupported database URL options")
		}
	}
	if timeout := options.Get("connect_timeout"); timeout != "" {
		if seconds, err := strconv.ParseUint(timeout, 10, 16); err != nil || seconds == 0 || seconds > 60 {
			return "", errors.New("database connection timeout must be 1 to 60 seconds")
		}
	} else {
		options.Set("connect_timeout", "5") // 沿用五秒连接上限，启动不会无限等待
	}

	host := connection.Hostname() // 只有明确的本机套接字可以覆盖 URL 主机
	if override := options.Get("host"); override != "" {
		if !strings.HasPrefix(override, "/") {
			return "", errors.New("host override is only for an explicit Unix socket directory")
		}
		host = override // Unix socket 目录由操作者明确选择
	}
	if options.Get("sslmode") == "" {
		options.Set("sslmode", "verify-full") // 远程连接默认校验证书和主机名
	}
	switch options.Get("sslmode") {
	case "verify-full": // 标准模式始终验证数据库身份
	case "disable":
		ip := net.ParseIP(host) // 不用 DNS 把远程名字当成本机地址
		if host != "localhost" && !strings.HasPrefix(host, "/") && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("unencrypted database connection is allowed only for local disposable environments")
		}
	default:
		return "", errors.New("database TLS must verify the server identity")
	}

	password, _ := connection.User.Password() // 显式写入空密码也可阻止外部凭据来源补值
	options.Set("password", password)
	options.Set("passfile", os.DevNull)    // 不读取当前用户的 pgpass 文件
	options.Set("servicefile", os.DevNull) // 不读取当前用户的连接服务定义
	options.Set("sslcert", "")             // 不自动读取客户端证书
	options.Set("sslkey", "")              // 不自动读取客户端私钥
	options.Set("sslrootcert", "")         // 证书信任沿用既有显式连接策略
	connection.RawQuery = options.Encode()
	return connection.String(), nil // 连接动作在 server.go，配置校验保持无网络副作用
}
