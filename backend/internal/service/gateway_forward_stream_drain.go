package service

import (
	"bufio"
	"errors"
	"io"
	"sync"
	"time"
)

// Anthropic 通常在 message_stop 前通过 message_delta 报告 usage，但兼容上游
// 可能在 stop 后补发 usage。固定尾窗口保留这部分容错，同时避免恢复无限等 EOF
// 的旧问题；窗口之外才到达的 usage 不保证被收集。
const gatewayForwardStreamTailGrace = 500 * time.Millisecond

// gatewayForwardStreamDrain 通过独立行泵读取上游，并接管响应 body 的关闭。
// terminal、闲置超时或读错退出时，关闭 body 并等待行泵结束，避免脱离客户端取消
// 的上游读协程继续阻塞。只有 handler 协程消费事件和操作下游 writer。
type gatewayForwardStreamDrain struct {
	pump         *gatewayForwardLinePump
	body         io.ReadCloser
	tailDeadline time.Time
	tailTimer    *time.Timer
}

func newGatewayForwardStreamDrain(scanner *bufio.Scanner, body io.ReadCloser, interval time.Duration) *gatewayForwardStreamDrain {
	return &gatewayForwardStreamDrain{
		pump: newGatewayForwardLinePump(scanner, interval),
		body: body,
	}
}

func (s *GatewayService) forwardStreamInterval() time.Duration {
	if s == nil || s.cfg == nil {
		// Preserve the fork's disabled connected-stream timeout when unconfigured.
		return 0
	}
	if s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		return time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	return 0 // 与 native 路径一致，保留显式禁用语义。
}

// startTerminalTail 首次收到 message_stop 时启动固定尾窗口；后续 terminal、
// keepalive 或 usage 都不能延长总期限。
func (d *gatewayForwardStreamDrain) startTerminalTail() {
	if d.tailTimer != nil {
		return
	}
	d.tailDeadline = time.Now().Add(gatewayForwardStreamTailGrace)
	d.tailTimer = time.NewTimer(gatewayForwardStreamTailGrace)
}

func (d *gatewayForwardStreamDrain) terminalSeen() bool {
	return d.tailTimer != nil
}

func (d *gatewayForwardStreamDrain) next() (string, error) {
	var tailCh, idleCh <-chan time.Time
	if d.tailTimer != nil {
		// 持续到达的事件不能因 select 总选中队列而拖长尾窗口。
		if !time.Now().Before(d.tailDeadline) {
			return "", io.EOF
		}
		tailCh = d.tailTimer.C
	}
	if d.pump.timer != nil {
		idleCh = d.pump.timer.C
	}
	select {
	case ev, ok := <-d.pump.events:
		if !ok {
			return "", io.EOF
		}
		d.pump.resetTimer()
		return ev.line, ev.err
	case <-tailCh:
		return "", io.EOF
	case <-idleCh:
		return "", errGatewayForwardStreamIdle
	}
}

func (d *gatewayForwardStreamDrain) stop() {
	d.pump.stop()
	if d.tailTimer != nil {
		d.tailTimer.Stop()
	}
	if d.body != nil {
		_ = d.body.Close() // 先解除正在进行的 Scan 阻塞，再等行泵退出。
	}
	for range d.pump.events {
	}
}

var errGatewayForwardStreamIdle = errors.New("stream data interval timeout")

type gatewayForwardLineEvent struct {
	line string
	err  error
}
type gatewayForwardLinePump struct {
	events   chan gatewayForwardLineEvent
	done     chan struct{}
	once     sync.Once
	timer    *time.Timer
	interval time.Duration
}

func newGatewayForwardLinePump(scanner *bufio.Scanner, interval time.Duration) *gatewayForwardLinePump {
	p := &gatewayForwardLinePump{events: make(chan gatewayForwardLineEvent), done: make(chan struct{}), interval: interval}
	if interval > 0 {
		p.timer = time.NewTimer(interval)
	}
	go func() {
		defer close(p.events)
		for scanner.Scan() {
			select {
			case p.events <- gatewayForwardLineEvent{line: scanner.Text()}:
			case <-p.done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case p.events <- gatewayForwardLineEvent{err: err}:
			case <-p.done:
			}
		}
	}()
	return p
}
func (p *gatewayForwardLinePump) resetTimer() {
	if p.timer == nil {
		return
	}
	if !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
	p.timer.Reset(p.interval)
}
func (p *gatewayForwardLinePump) stop() {
	p.once.Do(func() { close(p.done) })
	if p.timer != nil {
		p.timer.Stop()
	}
}
