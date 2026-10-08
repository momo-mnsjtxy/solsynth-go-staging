#!/usr/bin/env python3
"""Reject malformed workflow YAML and duplicate mapping keys before upload."""
from pathlib import Path
import yaml

class UniqueLoader(yaml.BaseLoader):
    pass

def mapping(loader, node, deep=False):
    result = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in result:
            raise ValueError(f"duplicate workflow key {key!r} at line {key_node.start_mark.line + 1}")
        result[key] = loader.construct_object(value_node, deep=deep)
    return result

UniqueLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, mapping)
root = Path(__file__).resolve().parents[1]
paths = sorted((root / ".github/workflows").glob("*.y*ml"))
for path in paths:
    doc = yaml.load(path.read_text(), Loader=UniqueLoader)
    if not isinstance(doc, dict) or "on" not in doc or "jobs" not in doc:
        raise SystemExit(f"{path}: workflow triggers/jobs missing")
for broken in ["env:\n  A: a\nenv:\n  B: b\n", "steps:\n- env:\n    A: a\n  env:\n    B: b\n", "run: pip install --only-binary=:all: --no-deps\n"]:
    try:
        yaml.load(broken, Loader=UniqueLoader)
    except (ValueError, yaml.YAMLError):
        pass
    else:
        raise SystemExit("invalid-workflow negative control accepted")
print(f"Validated {len(paths)} workflows with unique mapping keys; three malformed/duplicate controls rejected")
