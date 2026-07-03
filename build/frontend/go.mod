module github.com/sopranoworks/gitcote/build/frontend

go 1.26.2

require (
	github.com/evanw/esbuild v0.28.1
	github.com/sopranoworks/npmgo v0.4.1
	github.com/sopranoworks/npmgo/esbuildplugin v0.4.1
)

require golang.org/x/sys v0.0.0-20220715151400-c0bba94af5f8 // indirect

replace (
	github.com/sopranoworks/npmgo => /home/takahashi/Documents/src/npmgo
	github.com/sopranoworks/npmgo/esbuildplugin => /home/takahashi/Documents/src/npmgo/esbuildplugin
)
