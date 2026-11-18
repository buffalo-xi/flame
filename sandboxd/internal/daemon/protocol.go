package daemon

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// worker 进程 stdio 上的帧格式：4 字节大端长度前缀 + JSON。
// 与各语言 worker 脚本（worker.lua / worker.py / worker.js）的约定一致。

const maxMsgSize = 10 * 1024 * 1024 // 10MB

// sendMsg 发送消息：4字节大端长度前缀 + JSON
func sendMsg(w io.Writer, msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}

	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// recvMsg 接收消息：4字节大端长度前缀 + JSON
// 使用 json.Number 保持数字精度
func recvMsg(r io.Reader) (map[string]any, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	length := binary.BigEndian.Uint32(header)
	if length > maxMsgSize {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var msg map[string]any
	if err := decoder.Decode(&msg); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	return msg, nil
}
