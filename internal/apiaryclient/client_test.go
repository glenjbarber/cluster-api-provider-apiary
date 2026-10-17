package apiaryclient

import (
 "context"
 "fmt"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
)

func TestDeleteVMOnlyIgnoresExactMissingID(t *testing.T) {
 const id = "vm-123"
 tests := []struct{name string; status int; message string; wantError bool}{
  {"missing id",400,fmt.Sprintf("DeleteVM: id %q does not exist",id),false},
  {"not found",404,"",false},
  {"unrelated missing resource",400,"network does not exist",true},
  {"wrong vm",400,fmt.Sprintf("DeleteVM: id %q does not exist", "other"),true},
  {"server failure",503,fmt.Sprintf("DeleteVM: id %q does not exist",id),true},
  {"other bad request",400,"invalid request",true},
 }
 for _,tc:=range tests { t.Run(tc.name,func(t *testing.T){
  ts:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   if r.Method!=http.MethodDelete || r.URL.Path!="/v1/vms/"+id {t.Errorf("unexpected request %s %s",r.Method,r.URL.Path)}
   w.Header().Set("Content-Type","application/json");w.WriteHeader(tc.status)
   fmt.Fprintf(w,`{"error":%q}`,tc.message)
  }))
  defer ts.Close()
  err:=NewClient(ts.URL,"",nil).DeleteVM(context.Background(),id)
  if (err!=nil)!=tc.wantError {t.Fatalf("DeleteVM error = %v; wantError = %v",err,tc.wantError)}
  if tc.wantError && !strings.Contains(err.Error(),"apiaryclient: HTTP ") {t.Errorf("missing status in error: %v",err)}
 }) }
}
