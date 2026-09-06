#!/usr/bin/env python3
"""Catch newly added public CLI commands missing from the documentation reference."""
from pathlib import Path
import re
import subprocess
import sys
root = Path(__file__).resolve().parents[1]
help_text = subprocess.check_output(['go', 'run', './cmd/tailmux', 'help'], cwd=root, text=True)
commands = set(re.findall(r'^\s+(tailmux [a-z]+(?: [a-z]+)?)\b', help_text, re.MULTILINE))
reference = (root / 'docs/content/docs/commands.mdx').read_text()
missing = sorted(command for command in commands if command not in reference)
if missing:
    print('Document these public commands in docs/content/docs/commands.mdx:', *missing, sep='\n  ', file=sys.stderr)
    sys.exit(1)
print(f'Docs cover all {len(commands)} public commands in CLI help.')
