"""Verify that catalog entries ship reproducible, versioned bundles."""
import json
from pathlib import Path
from zipfile import ZipFile

root = Path(__file__).resolve().parent
catalog = json.loads((root / 'catalog.json').read_text())
assert catalog
assert len({item['id'] for item in catalog}) == len(catalog)
for item in catalog:
    source = root / 'recipes' / item['id']
    files = item.get('projectFiles', ['config.yaml', 'input.txt', *item.get('fixtures', []), *item.get('supportFiles', [])])
    with ZipFile(root / 'downloads' / (item['id'] + '.zip')) as bundle:
        assert set(bundle.namelist()) == {'README.md', 'example-output.md', *files}
        assert item['version'] in bundle.read('README.md').decode()
        if 'config.yaml' in files:
            assert item['version'] in bundle.read('config.yaml').decode()
        assert bundle.read('example-output.md').strip()
        for name in bundle.namelist():
            assert bundle.read(name) == (source / name).read_bytes(), (item['id'], name)
print(f'{len(catalog)} versioned recipe bundles verified.')
