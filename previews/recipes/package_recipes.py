"""Build the checked-in downloads from their reviewed source files."""
import json
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

root = Path(__file__).resolve().parent
downloads = root / 'downloads'
downloads.mkdir(exist_ok=True)
for recipe in json.loads((root / 'catalog.json').read_text()):
    slug = recipe['id']
    source = root / 'recipes' / slug
    files = recipe.get('projectFiles', ['config.yaml', 'input.txt', *recipe.get('fixtures', []), *recipe.get('supportFiles', [])])
    with ZipFile(downloads / f'{slug}.zip', 'w') as bundle:
        for filename in ('README.md', 'example-output.md', *files):
            bundle.writestr(ZipInfo(filename), (source / filename).read_bytes(), ZIP_DEFLATED)
