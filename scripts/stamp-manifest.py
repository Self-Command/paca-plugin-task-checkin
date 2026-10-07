"""Build metadata only; invoked by Actions, never used to inject credentials."""
import json,os,pathlib,re
root=pathlib.Path(__file__).resolve().parent.parent
sha=os.environ['GITHUB_SHA']
assert re.fullmatch('[0-9a-f]{40}',sha)
manifest=json.loads((root/'plugin.json').read_text())
for section in ['frontend','mcp']:
    if section in manifest and 'remoteEntryUrl' in manifest[section]:
        manifest[section]['remoteEntryUrl']=manifest[section]['remoteEntryUrl'].split('?')[0]+'?v='+sha
target=root/'release/wasm'/manifest['id']/'plugin.json'
target.parent.mkdir(parents=True,exist_ok=True)
target.write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
