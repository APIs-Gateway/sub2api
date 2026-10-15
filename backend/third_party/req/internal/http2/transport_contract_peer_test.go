// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO-BSD file at the req module root.
//
// Wire harness for the behavioral subset of golang/net/http2/transport_test.go
// at 18ece0ce30bc35fa81fe72028bf309bb3ff3f4a5. The client is req's private
// Transport; x/net supplies only the independent wire peer's frame codec.
package http2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/transport"
	xhttp2 "golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

type reqH2ContractFrame struct {
	kind xhttp2.FrameType
	id uint32
	end, ack bool
	code xhttp2.ErrCode
	data []byte
	fields []hpack.HeaderField
	err error
}

type reqH2ContractPeer struct {
	t *testing.T
	fixture *reqH2ContractFixture
	cc *ClientConn
	pending []reqH2ContractFrame
	client, server net.Conn
	writer *xhttp2.Framer
	frames chan reqH2ContractFrame
	done chan struct{}
}

type reqH2ContractFixture struct {
	t *testing.T
	tr *Transport
	dials chan *reqH2ContractPeer
	mu sync.Mutex
	peers []*reqH2ContractPeer
}

type reqH2ContractResult struct {
	response *http.Response
	body []byte
	err error
}

type reqH2ContractCall struct {
	result chan reqH2ContractResult
	headers chan *http.Response
	done chan struct{}
	cancel context.CancelFunc
}

func newReqH2ContractFixture(t *testing.T) *reqH2ContractFixture {
	t.Helper()
	f := &reqH2ContractFixture{t:t, dials:make(chan *reqH2ContractPeer,16)}
	f.tr = &Transport{Options:&transport.Options{}}
	f.tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn,error) {
		if err:=ctx.Err(); err!=nil { return nil,err }
		client,server:=net.Pipe()
		deadline:=time.Now().Add(20*time.Second)
		client.SetDeadline(deadline)
		server.SetDeadline(deadline)
		p:=&reqH2ContractPeer{t:t,fixture:f,client:client,server:server,writer:xhttp2.NewFramer(server,nil),frames:make(chan reqH2ContractFrame,128),done:make(chan struct{})}
		f.mu.Lock(); f.peers=append(f.peers,p); f.mu.Unlock()
		go p.read()
		select {
		case f.dials<-p:return client,nil
		case <-ctx.Done():client.Close();server.Close();return nil,ctx.Err()
		}
	}
	t.Cleanup(func(){
		f.mu.Lock();peers:=append([]*reqH2ContractPeer(nil),f.peers...);f.mu.Unlock()
		for _,p:=range peers { p.client.Close();p.server.Close() }
		for _,p:=range peers {
			select {case <-p.done:case <-time.After(3*time.Second):t.Error("HTTP/2 contract peer did not join")}
			if p.cc != nil { select {case <-p.cc.readerDone:case <-time.After(3*time.Second):t.Error("private HTTP/2 read loop did not join")} }
		}
		f.tr.CloseIdleConnections()
	})
	return f
}

func (p *reqH2ContractPeer) read() {
	defer close(p.done)
	fail:=func(err error){select{case p.frames<-reqH2ContractFrame{err:err}:default:}}
	preface:=make([]byte,len(xhttp2.ClientPreface))
	if _,err:=io.ReadFull(p.server,preface);err!=nil {fail(err);return}
	if string(preface)!=xhttp2.ClientPreface {fail(fmt.Errorf("invalid HTTP/2 preface %q",preface));return}
	fr:=xhttp2.NewFramer(io.Discard,p.server)
	fr.ReadMetaHeaders=hpack.NewDecoder(4096,nil)
	for {
		frame,err:=fr.ReadFrame()
		if err!=nil {fail(err);return}
		e:=reqH2ContractFrame{kind:frame.Header().Type,id:frame.Header().StreamID}
		switch frame:=frame.(type) {
		case *xhttp2.MetaHeadersFrame:e.end=frame.StreamEnded();e.fields=append([]hpack.HeaderField(nil),frame.Fields...)
		case *xhttp2.DataFrame:e.end=frame.StreamEnded();e.data=append([]byte(nil),frame.Data()...)
		case *xhttp2.SettingsFrame:e.ack=frame.IsAck()
		case *xhttp2.PingFrame:e.ack=frame.IsAck();e.data=append([]byte(nil),frame.Data[:]...)
		case *xhttp2.GoAwayFrame:e.code=frame.ErrCode;e.id=frame.LastStreamID;e.data=append([]byte(nil),frame.DebugData()...)
		case *xhttp2.RSTStreamFrame:e.code=frame.ErrCode
		}
		select {case p.frames<-e:case <-time.After(10*time.Second):fail(errors.New("peer event buffer was not drained"));return}
	}
}

func (f *reqH2ContractFixture) peer() *reqH2ContractPeer {
	f.t.Helper()
	select {
	case p:=<-f.dials:
		p.wait(func(e reqH2ContractFrame)bool{return e.kind==xhttp2.FrameSettings&&!e.ack})
		p.must(p.writer.WriteSettings())
		p.wait(func(e reqH2ContractFrame)bool{return e.kind==xhttp2.FrameSettings&&e.ack})
		return p
	case <-time.After(5*time.Second):f.t.Fatal("expected actual private transport dial");return nil
	}
}

func (p *reqH2ContractPeer) must(err error) {
	p.t.Helper();if err!=nil {p.t.Fatalf("HTTP/2 peer write: %v",err)}
}

func (p *reqH2ContractPeer) wait(match func(reqH2ContractFrame)bool) reqH2ContractFrame {
	p.t.Helper()
	for i,e:=range p.pending {
		if match(e) {p.pending=append(p.pending[:i],p.pending[i+1:]...);return e}
	}
	guard:=time.NewTimer(5*time.Second);defer guard.Stop()
	for {select {
	case e:=<-p.frames:
		if e.err!=nil {p.t.Fatalf("unexpected peer read error: %v",e.err)}
		if match(e){return e}
		p.pending=append(p.pending,e)
	case <-guard.C:p.t.Fatal("HTTP/2 contract frame guard expired");return reqH2ContractFrame{}
	}}
}

func (p *reqH2ContractPeer) request(id uint32, method ...string) reqH2ContractFrame {
	p.t.Helper()
	e:=p.wait(func(e reqH2ContractFrame)bool{return e.kind==xhttp2.FrameHeaders})
	if e.id!=id {p.t.Fatalf("request stream=%d, want %d",e.id,id)}
	wantMethod := "GET"
	if len(method) == 1 { wantMethod = method[0] }
	want := map[string]string{":method":wantMethod, ":path":"/contracts", ":scheme":"https", ":authority":"wire.invalid"}
	actual := map[string]string{}
	for _, field := range e.fields { if _, ok := want[field.Name]; ok { actual[field.Name] = field.Value } }
	for key, value := range want { if actual[key] != value { p.t.Fatalf("stream %d header %s=%q, want %q", id, key, actual[key], value) } }
	if p.cc==nil {p.cc=p.fixture.clientConn(p)}
	return e
}

func (p *reqH2ContractPeer) upload(id uint32) []byte {
	p.t.Helper();var body []byte
	for {
		e:=p.wait(func(e reqH2ContractFrame)bool{return e.kind==xhttp2.FrameData&&e.id==id})
		body=append(body,e.data...);if e.end{return body}
	}
}

func (p *reqH2ContractPeer) headers(id uint32,end bool,fields ...hpack.HeaderField) {
	p.t.Helper();var b bytes.Buffer;enc:=hpack.NewEncoder(&b)
	for _,field:=range fields {p.must(enc.WriteField(field))}
	p.must(p.writer.WriteHeaders(xhttp2.HeadersFrameParam{StreamID:id,BlockFragment:b.Bytes(),EndHeaders:true,EndStream:end}))
}

func (p *reqH2ContractPeer) response(id uint32,body string) {
	p.t.Helper();p.headers(id,body=="",hpack.HeaderField{Name:":status",Value:"200"})
	if body!="" {p.must(p.writer.WriteData(id,true,[]byte(body)))}
}

func (p *reqH2ContractPeer) closed() {
	p.t.Helper()
	select {
	case <-p.done:
		for {
			select {
			case e:=<-p.frames:
				if e.err!=nil {
					if !errors.Is(e.err,io.EOF){p.t.Fatalf("peer closure=%v, want actual EOF",e.err)}
					return
				}
			default:p.t.Fatal("peer reader ended without EOF");return
			}
		}
	case <-time.After(5*time.Second):p.t.Fatal("peer did not observe connection EOF")
	}
}

func (f *reqH2ContractFixture) start(req *http.Request,cached bool) *reqH2ContractCall {
	f.t.Helper()
	ctx,cancel:=context.WithTimeout(req.Context(),12*time.Second)
	req=req.WithContext(ctx)
	c:=&reqH2ContractCall{result:make(chan reqH2ContractResult,1),headers:make(chan *http.Response,1),done:make(chan struct{}),cancel:cancel}
	go func(){
		defer close(c.done)
		var res *http.Response;var err error
		if cached {res,err=f.tr.RoundTripOpt(req,RoundTripOpt{OnlyCachedConn:true})} else {res,err=f.tr.RoundTrip(req)}
		var body []byte
		if err==nil {
			c.headers<-res
			body,err=io.ReadAll(res.Body)
			if closeErr:=res.Body.Close();err==nil {err=closeErr}
		}
		c.result<-reqH2ContractResult{res,body,err}
	}()
	f.t.Cleanup(func(){cancel();select{case <-c.done:case <-time.After(3*time.Second):f.t.Error("private RoundTrip/body reader did not join")}})
	return c
}

func reqH2ContractRequest(t *testing.T,method string,body io.Reader) *http.Request {
	t.Helper();req,err:=http.NewRequest(method,"https://wire.invalid/contracts",body)
	if err!=nil{t.Fatal(err)};return req
}

func (c *reqH2ContractCall) get(t *testing.T) reqH2ContractResult {
	t.Helper();select {case r:=<-c.result:return r;case <-time.After(5*time.Second):t.Fatal("private RoundTrip result guard expired");return reqH2ContractResult{}}
}

func (c *reqH2ContractCall) header(t *testing.T) *http.Response {
	t.Helper();select{case r:=<-c.headers:return r;case <-time.After(5*time.Second):t.Fatal("private RoundTrip did not return response headers");return nil}
}

func reqH2ContractOK(t *testing.T,r reqH2ContractResult,body string) {
	t.Helper();if r.err!=nil||r.response==nil||r.response.StatusCode!=200||string(r.body)!=body {t.Fatalf("response=%v body=%q err=%v, want 200/%q",r.response,r.body,r.err,body)}
}

func (f *reqH2ContractFixture) dialCount() int {f.mu.Lock();defer f.mu.Unlock();return len(f.peers)}

func (f *reqH2ContractFixture) clientConn(p *reqH2ContractPeer) *ClientConn {
	f.t.Helper();pool:=f.tr.connPool().(*clientConnPool);pool.mu.Lock();defer pool.mu.Unlock()
	for cc:=range pool.keys {if cc.tconn==p.client{return cc}}
	f.t.Fatal("active wire connection missing from private pool");return nil
}
