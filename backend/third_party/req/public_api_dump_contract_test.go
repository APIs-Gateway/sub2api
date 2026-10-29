package req_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	req "github.com/imroc/req/v3"
)

type publicAPIWriter struct { mu sync.Mutex; buffer bytes.Buffer; closed bool }
func (w *publicAPIWriter) Write(p []byte)(int,error){w.mu.Lock();defer w.mu.Unlock();if w.closed{return 0,errors.New("caller writer closed")};return w.buffer.Write(p)}
func (w *publicAPIWriter) Close()error{w.mu.Lock();defer w.mu.Unlock();w.closed=true;return nil}
func (w *publicAPIWriter) String()string{w.mu.Lock();defer w.mu.Unlock();return w.buffer.String()}
func (w *publicAPIWriter) isClosed()bool{w.mu.Lock();defer w.mu.Unlock();return w.closed}

func TestReqPublicAPI_DumpVisibility(t *testing.T) {
	type visibility struct { requestHeader,requestBody,responseHeader,responseBody bool }
	all:=visibility{true,true,true,true}
	cases:=[]struct{name string; scope string; configure func(*publicAPIWriter)*req.Request; want visibility}{
		{"client_all","client",func(w *publicAPIWriter)*req.Request{req.EnableDumpAllTo(w);return req.R()},all},
		{"client_async","client",func(w *publicAPIWriter)*req.Request{req.EnableDumpAllAsync();return req.R()},all},
		{"client_headers","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutBody();return req.R()},visibility{true,false,true,false}},
		{"client_bodies","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutHeader();return req.R()},visibility{false,true,false,true}},
		{"client_request_only","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutResponse();return req.R()},visibility{true,true,false,false}},
		{"client_response_only","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutRequest();return req.R()},visibility{false,false,true,true}},
		{"client_without_upload","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutRequestBody();return req.R()},visibility{true,false,true,true}},
		{"client_without_download","client",func(*publicAPIWriter)*req.Request{req.EnableDumpAllWithoutResponseBody();return req.R()},visibility{true,true,true,false}},
		{"each_all","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequest();return req.R()},all},
		{"each_headers","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutBody();return req.R()},visibility{true,false,true,false}},
		{"each_bodies","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutHeader();return req.R()},visibility{false,true,false,true}},
		{"each_request_only","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutResponse();return req.R()},visibility{true,true,false,false}},
		{"each_response_only","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutRequest();return req.R()},visibility{false,false,true,true}},
		{"each_without_upload","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutRequestBody();return req.R()},visibility{true,false,true,true}},
		{"each_without_download","each",func(*publicAPIWriter)*req.Request{req.EnableDumpEachRequestWithoutResponseBody();return req.R()},visibility{true,true,true,false}},
		{"request_all","request",func(*publicAPIWriter)*req.Request{return req.EnableDump()},all},
		{"request_headers","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutBody()},visibility{true,false,true,false}},
		{"request_bodies","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutHeader()},visibility{false,true,false,true}},
		{"request_request_only","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutResponse()},visibility{true,true,false,false}},
		{"request_response_only","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutRequest()},visibility{false,false,true,true}},
		{"request_without_upload","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutRequestBody()},visibility{true,false,true,true}},
		{"request_without_download","request",func(*publicAPIWriter)*req.Request{return req.EnableDumpWithoutResponseBody()},visibility{true,true,true,false}},
		{"request_options","writer",func(w *publicAPIWriter)*req.Request{return req.SetDumpOptions(&req.DumpOptions{Output:w,RequestBody:true,ResponseBody:true}).EnableDump()},visibility{false,true,false,true}},
	}
	for _,tc:=range cases {t.Run(tc.name,func(t *testing.T){
		client:=publicAPIInstall(t);peer:=publicAPINewPeer(t,func(int,publicAPIExchange)(int,string,string){return 200,"text/plain","PUBLIC-RESPONSE-BODY-8"})
		writer:=new(publicAPIWriter)
		req.SetCommonDumpOptions(&req.DumpOptions{Output:writer,RequestHeader:true,RequestBody:true,ResponseHeader:true,ResponseBody:true})
		r:=tc.configure(writer).SetHeader("X-Public-Request","request-header-token").SetBodyString("PUBLIC-REQUEST-BODY-9")
		response,err:=r.Post(peer.server.URL+"/visibility");publicAPIResponse(t,response,err,200,"PUBLIC-RESPONSE-BODY-8");e:=peer.receive(t)
		if e.body!="PUBLIC-REQUEST-BODY-9" || e.headers.Get("X-Public-Request")!="request-header-token" || peer.requests.Load()!=1 {t.Fatal("dump prerequisite request differs")}
		if err:=r.CloseDump();err!=nil {t.Fatal(err)};if err:=client.CloseDump();err!=nil {t.Fatal(err)}
		dump:=response.Dump();if tc.scope=="client" || tc.scope=="writer" {dump=writer.String()}
		actual:=visibility{strings.Contains(dump,"request-header-token"),strings.Contains(dump,"PUBLIC-REQUEST-BODY-9"),strings.Contains(dump,"response-header-token"),strings.Contains(dump,"PUBLIC-RESPONSE-BODY-8")}
		if actual!=tc.want {t.Fatalf("dump visibility got=%+v want=%+v raw=%q",actual,tc.want,dump)}
		if writer.isClosed() {t.Fatal("CloseDump adopted caller writer")};if n,err:=writer.Write([]byte("caller-after-drain"));err!=nil || n!=len("caller-after-drain") {t.Fatal("caller writer unusable after dump drain")}
	})}
}
