// SPDX-License-Identifier: AGPL-3.0-only
/*
可信公钥：从明确指定的 PEM 文件读取唯一 RSA 公钥，供令牌验证使用。
配置校验调用 publicKey；例如 key, err := publicKey("/run/keys/access-public.pem")。
同时支持 PKCS#1 和 PKIX 公钥，拒绝私钥、证书、多个 PEM 和超过 32 KiB 的文件。
*/
package main

import (
	"crypto/rsa"   // 令牌验证器需要 RSA 公钥
	"crypto/x509"  // 解读两种标准公钥格式
	"encoding/pem" // 从 PEM 文本取出公钥数据
	"errors"       // 返回不含文件内容的配置错误
	"io"           // 限制读取量
	"os"           // 打开调用方明确提供的文件
	"strings"      // 允许 PEM 外的空白，拒绝额外内容
)

// publicKey 读取验证用公钥；这里是外部文件边界，所有格式检查都集中在此。
func publicKey(path string) (*rsa.PublicKey, error) {
	file, err := os.Open(path) // 不搜索默认密钥目录，也不自动生成密钥
	if err != nil {
		return nil, errors.New("trusted public key file unavailable")
	}
	defer file.Close() // 解读结束就归还文件句柄

	data, err := io.ReadAll(io.LimitReader(file, 32769)) // 多读一个字节才能识别超限文件
	if err != nil || len(data) > 32768 {
		return nil, errors.New("invalid public key file")
	}
	block, rest := pem.Decode(data) // 一个文件必须只表达一把信任公钥
	if block == nil || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errors.New("one public key PEM required")
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes) // 兼容 RSA 专用的 PKCS#1 公钥
	}
	if block.Type != "PUBLIC KEY" {
		return nil, errors.New("public key PEM required; private keys and certificates are not accepted")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes) // 通用 PKIX 格式还需确认算法
	if err != nil {
		return nil, errors.New("invalid public key")
	}
	rsaKey, ok := key.(*rsa.PublicKey) // 验证策略不接受其它算法
	if !ok {
		return nil, errors.New("RSA public key required")
	}
	return rsaKey, nil // 只把公钥交给调用方，文件不会继续保持打开
}
