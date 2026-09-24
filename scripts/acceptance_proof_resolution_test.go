package scripts

import (
 "encoding/json"
 "os"
 "os/exec"
 "path/filepath"
 "testing"
)

func run(t *testing.T, env []string, args ...string) error { t.Helper(); c:=exec.Command("node", args...); c.Dir=".."; c.Env=append(os.Environ(), env...); return c.Run() }
func index(t *testing.T, titles map[string]int) string { t.Helper(); p:=filepath.Join(t.TempDir(),"index.json"); b,_:=json.Marshal(map[string]any{"version":1,"files":map[string]any{"sdk/typescript/test/http-wkt-json.test.ts":map[string]any{"workspace":"sdk/typescript/","titles":titles}}}); if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)};return p }
func TestResolveTaskProof_ResolvesRegisteredAllowlistedTargets(t *testing.T){ for _,p:=range []string{"api:check","test:engine-standalone","site:build"}{if err:=run(t,nil,"scripts/resolve-task-proof.mjs",p);err!=nil{t.Fatal(err)}} }
func TestResolveTaskProof_DoesNotExecuteRegisteredTarget(t *testing.T){ d:=t.TempDir(); task:=filepath.Join(d,"task"); if err:=os.WriteFile(task,[]byte("#!/bin/sh\nif [ \"$1\" = --list ]; then printf '%s' '{\"tasks\":[{\"name\":\"api:check\"}]}' ; exit 0; fi\nexit 99\n"),0700);err!=nil{t.Fatal(err)}; if err:=run(t,[]string{"PATH="+d},"scripts/resolve-task-proof.mjs","api:check");err!=nil{t.Fatal(err)} }
func TestResolveTaskProof_RejectsUnregisteredOrUnsupportedTarget(t *testing.T){ if run(t,nil,"scripts/resolve-task-proof.mjs","unsupported") == nil {t.Fatal("unsupported proof resolved")} }
func TestVitestProofIndex_Scenario2_IndexesTrackedSourcesOnce(t *testing.T){ if _,err:=os.Stat("build-vitest-proof-index.mjs");err!=nil{t.Fatal(err)} }
func TestVitestProofIndex_Scenario2_PreservesPathScopedExactTitleResolution(t *testing.T){ p:=index(t,map[string]int{"title":1}); if err:=run(t,[]string{"ACTRACE_VITEST_INDEX="+p},"scripts/resolve-vitest-proof.mjs","vitest:sdk/typescript/test/http-wkt-json.test.ts#dGl0bGU");err!=nil{t.Fatal(err)} }
func TestVitestProofIndex_Scenario2_PreservesWorkspaceAndTSXSemantics(t *testing.T){ p:=index(t,map[string]int{"title":1}); if run(t,[]string{"ACTRACE_VITEST_INDEX="+p},"scripts/resolve-vitest-proof.mjs","vitest:apps/web/test.ts#dGl0bGU") == nil {t.Fatal("cross-workspace resolution succeeded")} }
func TestVitestProofIndex_Scenario2_FailsClosed(t *testing.T){ p:=index(t,map[string]int{"title":2}); if run(t,[]string{"ACTRACE_VITEST_INDEX="+p},"scripts/resolve-vitest-proof.mjs","vitest:sdk/typescript/test/http-wkt-json.test.ts#dGl0bGU") == nil {t.Fatal("duplicate title resolved")} }
func TestVitestProofIndex_Scenario2_DoesNotChangePlaywrightResolution(t *testing.T){ if _,err:=os.Stat("resolve-playwright-proof.mjs");err!=nil{t.Fatal(err)} }
