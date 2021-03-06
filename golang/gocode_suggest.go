package golang

import (
	"github.com/mdempsky/gocode/suggest"
	"go/build"
	"go/importer"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/gcexportdata"
	"margo.sh/mg"
	"runtime"
	"runtime/debug"
	"sync"
)

type gsImpKey struct {
	path string
	dir  string
}

type gsuOpts struct {
	ProposeBuiltins bool
	Debug           bool
	Source          bool
}

type gcSuggest struct {
	gsuOpts
	sync.Mutex
	grImp types.ImporterFrom
	cache map[gsImpKey]*types.Package
}

func newGcSuggest(o gsuOpts) *gcSuggest {
	gsu := &gcSuggest{gsuOpts: o}
	gsu.init()
	return gsu
}

func (gsu *gcSuggest) init() {
	gsu.cache = map[gsImpKey]*types.Package{}
	gsu.grImp = gsu.newImporter()
}

func (gsu *gcSuggest) newImporter() types.ImporterFrom {
	// TODO: switch to source importer only
	switch {
	case gsu.Source:
		return importer.For("source", nil).(types.ImporterFrom)
	case runtime.Compiler == "gc":
		return gcexportdata.NewImporter(token.NewFileSet(), map[string]*types.Package{})
	default:
		return importer.Default().(types.ImporterFrom)
	}
}

func (gsu *gcSuggest) importer(mx *mg.Ctx) types.ImporterFrom {
	return &gsuImporter{
		mx:  mx,
		bld: BuildContext(mx),
		gsu: gsu,
		imp: gsu.newImporter(),
	}
}

func (gsu *gcSuggest) candidates(mx *mg.Ctx) []suggest.Candidate {
	defer mx.Profile.Push("candidates").Pop()
	gsu.Lock()
	defer gsu.Unlock()

	defer func() {
		if e := recover(); e != nil {
			mx.Log.Printf("gocode/suggest panic: %s\n%s\n", e, debug.Stack())
		}
	}()

	cfg := suggest.Config{
		Importer:   gsu.importer(mx),
		Builtin:    gsu.ProposeBuiltins,
		IgnoreCase: true,
	}
	if gsu.Debug {
		cfg.Logf = func(f string, a ...interface{}) {
			gsu.dbgf(mx, f, a...)
		}
	}

	v := mx.View
	src, _ := v.ReadAll()
	if len(src) == 0 {
		return nil
	}

	l, _ := cfg.Suggest(v.Filename(), src, v.Pos)
	return l
}

func (gsu *gcSuggest) dbgf(mx *mg.Ctx, f string, a ...interface{}) {
	if !gsu.Debug {
		return
	}

	mx.Log.Dbg.Printf("Gocode: "+f, a...)
}

type gsuImporter struct {
	mx  *mg.Ctx
	bld *build.Context
	gsu *gcSuggest
	imp types.ImporterFrom
}

func (gi *gsuImporter) Import(path string) (*types.Package, error) {
	return gi.ImportFrom(path, ".", 0)
}

func (gi *gsuImporter) ImportFrom(impPath, srcDir string, mode types.ImportMode) (*types.Package, error) {
	cache := gi.gsu.cache
	impKey := gsImpKey{
		path: impPath,
		dir:  srcDir,
	}
	if p, ok := cache[impKey]; ok {
		return p, nil
	}

	defer gi.mx.Profile.Push(impPath).Pop()

	bpkg, err := gi.bld.Import(impPath, srcDir, 0)
	if err != nil {
		gi.gsu.dbgf(gi.mx, "build.Import(%q, %q): %s\n", impPath, srcDir, err)
		return nil, err
	}

	imp := gi.imp
	if bpkg.Goroot {
		if bpkg.ImportPath == "unsafe" {
			return types.Unsafe, nil
		}
		imp = gi.gsu.grImp
	}

	p, err := imp.ImportFrom(impPath, srcDir, mode)
	if err == nil && p.Complete() && bpkg.Goroot {
		cache[impKey] = p
	}
	if err != nil {
		gi.gsu.dbgf(gi.mx, "%T.ImportFrom(%q, %q): %s\n", imp, impPath, srcDir, err)
	}

	return p, err
}
