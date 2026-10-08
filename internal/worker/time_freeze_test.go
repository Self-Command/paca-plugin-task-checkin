package worker
import("net/http/httptest";"testing")
func TestTimeFreezeRejectsAnonymousWithoutSideEffects(t *testing.T){w:=&Worker{ActionSecret:"private"};r:=httptest.NewRequest("POST","/internal/v1/times/freeze",nil);out:=httptest.NewRecorder();w.Handler().ServeHTTP(out,r);if out.Code!=401{t.Fatal(out.Code)}}
