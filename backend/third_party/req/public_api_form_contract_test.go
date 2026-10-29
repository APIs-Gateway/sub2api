package req_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	req "github.com/imroc/req/v3"
)

func TestReqPublicAPI_FormsAndFiles(t *testing.T) {
	for _,name:=range []string{"ordered_fields","typed_form_values","common_repeated_form"} {t.Run(name,func(t *testing.T){
		publicAPIInstall(t);peer:=publicAPIPlainPeer(t);var r *req.Request;var want url.Values
		switch name {
		case "ordered_fields":r=req.SetOrderedFormData("z","last first","a","A & B","z","second");want=url.Values{"z":{"last first","second"},"a":{"A & B"}}
		case "typed_form_values":r=req.SetFormDataAnyType(map[string]any{"count":3,"enabled":true}).SetFormData(map[string]string{"name":"A + B"});want=url.Values{"count":{"3"},"enabled":{"true"},"name":{"A + B"}}
		case "common_repeated_form":req.SetCommonFormDataFromValues(url.Values{"tag":{"one","two"}});r=req.SetFormData(map[string]string{"local":"only"});want=url.Values{"tag":{"one","two"},"local":{"only"}}
		}
		response,err:=r.Post(peer.server.URL+"/form");publicAPIResponse(t,response,err,200,"public-api-response");e:=peer.receive(t);values,parseErr:=url.ParseQuery(e.body)
		if parseErr!=nil || !reflect.DeepEqual(values,want) || e.headers.Get("Content-Type")!="application/x-www-form-urlencoded" || peer.requests.Load()!=1 {t.Fatalf("form wire=%+v parsed=%v want=%v error=%v",e,values,want,parseErr)}
		if name=="ordered_fields" && e.body!="z=last+first&a=A+%26+B&z=second" {t.Fatalf("ordered form lost caller order: %q",e.body)}
	})}
	for _,name:=range []string{"file_reader","file_bytes","file_path_map","file_upload"} {t.Run(name,func(t *testing.T){
		publicAPIInstall(t);peer:=publicAPIPlainPeer(t);req.SetMultipartBoundaryFunc(func()string{return "public-api-owned-boundary"})
		payload:="bounded file\x00 payload +&";fileName:="payload.txt";var r *req.Request
		path:=filepath.Join(t.TempDir(),fileName)
		switch name {
		case "file_reader":r=req.SetFileReader("artifact",fileName,strings.NewReader(payload))
		case "file_bytes":r=req.SetFileBytes("artifact",fileName,[]byte(payload))
		case "file_path_map":if err:=os.WriteFile(path,[]byte(payload),0600);err!=nil {t.Fatal(err)};r=req.SetFiles(map[string]string{"artifact":path})
		case "file_upload":r=req.SetFileUpload(req.FileUpload{ParamName:"artifact",FileName:fileName,FileSize:int64(len(payload)),ContentType:"application/x-contract",GetFileContent:func()(io.ReadCloser,error){return io.NopCloser(strings.NewReader(payload)),nil}})
		}
		response,err:=r.SetFormDataFromValues(url.Values{"tag":{"one","two words"}}).Post(peer.server.URL+"/upload");publicAPIResponse(t,response,err,200,"public-api-response");e:=peer.receive(t)
		media,params,parseErr:=mime.ParseMediaType(e.headers.Get("Content-Type"));if parseErr!=nil || media!="multipart/form-data" || params["boundary"]!="public-api-owned-boundary" {t.Fatalf("multipart media=%q params=%v error=%v",media,params,parseErr)}
		reader:=multipart.NewReader(strings.NewReader(e.body),params["boundary"]);var tags []string;files:=0
		for {part,nextErr:=reader.NextPart();if nextErr==io.EOF {break};if nextErr!=nil {t.Fatal(nextErr)};body,readErr:=io.ReadAll(part);closeErr:=part.Close();if readErr!=nil || closeErr!=nil {t.Fatalf("part read=%v close=%v",readErr,closeErr)}
			if part.FormName()=="tag" {tags=append(tags,string(body))} else if part.FormName()=="artifact" {files++;if part.FileName()!=fileName || string(body)!=payload {t.Fatalf("actual file filename=%q body=%q",part.FileName(),body)};if name=="file_upload" && part.Header.Get("Content-Type")!="application/x-contract" {t.Fatal("explicit file media type lost")}} else {t.Fatalf("unexpected part=%v",part.Header)}
		}
		if !reflect.DeepEqual(tags,[]string{"one","two words"}) || files!=1 || e.method!="POST" || peer.requests.Load()!=1 {t.Fatalf("multipart tags=%v files=%d wire=%+v",tags,files,e)}
		if name=="file_path_map" {if err:=os.Remove(path);err!=nil {t.Fatalf("uploaded owned file retained open: %v",err)}}
	})}
	t.Run("missing_file",func(t *testing.T){
		publicAPIInstall(t);peer:=publicAPIPlainPeer(t);path:=filepath.Join(t.TempDir(),"absent")
		response,err:=req.SetFile("artifact",path).Post(peer.server.URL+"/must-not-arrive")
		var fileErr *os.PathError
		if response==nil || !errors.Is(err,os.ErrNotExist) || !errors.As(err,&fileErr) || fileErr.Path!=path || peer.requests.Load()!=0 {t.Fatalf("missing file identity response=%v error=%v pathError=%v hits=%d",response,err,fileErr,peer.requests.Load())}
	})
}

func TestReqPublicAPI_DownloadDestinations(t *testing.T) {
	for _,name:=range []string{"caller_writer","relative_output_file"} {t.Run(name,func(t *testing.T){
		publicAPIInstall(t);peer:=publicAPIPlainPeer(t);writer:=new(bytes.Buffer);directory:=t.TempDir();req.SetOutputDirectory(directory)
		var mu sync.Mutex;var progress []req.DownloadInfo
		callback:=func(info req.DownloadInfo){mu.Lock();progress=append(progress,info);mu.Unlock()}
		var r *req.Request
		if name=="caller_writer" {r=req.SetOutput(writer).SetDownloadCallbackWithInterval(callback,0)} else {r=req.SetOutputFile("response.bin").SetDownloadCallback(callback)}
		response,err:=r.Get(peer.server.URL+"/download")
		if err!=nil || response==nil || response.StatusCode!=200 {t.Fatalf("download response=%v error=%v",response,err)};peer.receive(t)
		if response.Body!=nil {if closeErr:=response.Body.Close();closeErr!=nil {t.Fatal(closeErr)}}
		if name=="caller_writer" {if writer.String()!="public-api-response" {t.Fatalf("caller destination bytes=%q",writer.String())};if n,writeErr:=writer.Write([]byte("-caller"));writeErr!=nil || n!=7 {t.Fatal("caller destination became unusable")}} else {
			path:=filepath.Join(directory,"response.bin");body,readErr:=os.ReadFile(path);if readErr!=nil || string(body)!="public-api-response" {t.Fatalf("output file body=%q error=%v",body,readErr)};if removeErr:=os.Remove(path);removeErr!=nil {t.Fatalf("output file retained after Do: %v",removeErr)}
		}
		mu.Lock();snapshot:=append([]req.DownloadInfo(nil),progress...);mu.Unlock()
		if len(snapshot)==0 || snapshot[len(snapshot)-1].DownloadedSize!=int64(len("public-api-response")) {t.Fatalf("download completion progress=%+v",snapshot)}
		for _,info:=range snapshot {if info.Response!=response || info.DownloadedSize<0 || info.DownloadedSize>int64(len("public-api-response")) {t.Fatalf("progress lost actual response identity: %+v",info)}}
	})}
}
