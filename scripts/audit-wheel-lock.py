"""Present direct wheel pins to pip-audit under their upstream release versions."""

import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit

from packaging.requirements import Requirement
from packaging.utils import canonicalize_name, parse_wheel_filename

for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if " @ " in line and not line.lstrip().startswith("#"):
        requirement = Requirement(line.removesuffix("\\").strip())
        name, version, _, _ = parse_wheel_filename(Path(unquote(urlsplit(requirement.url).path)).name)
        if name != canonicalize_name(requirement.name) or requirement.marker or requirement.extras:
            raise ValueError(f"unsupported direct wheel pin: {line}")
        line = f"{name}=={version.public}" + (" " + "\\" if line.endswith("\\") else "")
    print(line)
