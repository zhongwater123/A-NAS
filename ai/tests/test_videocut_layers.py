"""The layers of ai/anas_videocut (docs/architecture/montage-center.md, #639):
algo/ is pure computation and imports only the standard library, NumPy and
algo/ itself, so it behaves the same on the development machine and the NAS;
ops/ composes algo/ into one operation and does not import worker.py, which
receives and answers the requests.

Written by hand, not with a tool (AGENTS.md, 不重复造轮子): Ruff's banned-api
(TID251) and import-linter's forbidden contracts can only list the modules
that are banned, and neither can say "only these"; import-linter would also
add a dependency that has to pass the vulnerability check. The rules read
every import statement with ast, those inside functions too, and resolve
relative imports against the file's package."""

import ast
import sys
import unittest
from pathlib import Path

PACKAGE = "anas_videocut"
ROOT = Path(__file__).resolve().parents[1] / PACKAGE
# Besides the standard library, the one module algo/ may import.
ALGO_THIRD_PARTY = {"numpy"}


def module_name(path):
    """The dotted name of a file in the package: algo/fill.py is anas_videocut.algo.fill."""
    parts = path.relative_to(ROOT.parent).with_suffix("").parts
    return ".".join(parts[:-1] if parts[-1] == "__init__" else parts)


def imports(path):
    """Every import of a file, as (line, module, names): names are what a from-import takes from the module."""
    name = module_name(path)
    package = name if path.name == "__init__.py" else name.rpartition(".")[0]
    found = []
    for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"), str(path))):
        if isinstance(node, ast.Import):
            found.extend((node.lineno, alias.name, ()) for alias in node.names)
        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            if node.level:
                base = package.split(".")
                base = base[: len(base) - node.level + 1]
                module = ".".join([*base, module] if module else base)
            found.append((node.lineno, module, tuple(alias.name for alias in node.names)))
    return found


def within(module, prefix):
    return module == prefix or module.startswith(prefix + ".")


def algo_allowed(module):
    top = module.partition(".")[0]
    return top in sys.stdlib_module_names or top in ALGO_THIRD_PARTY or within(module, f"{PACKAGE}.algo")


def imports_worker(module, names):
    return within(module, f"{PACKAGE}.worker") or (module == PACKAGE and "worker" in names)


def violations(folder, bad):
    files = sorted((ROOT / folder).rglob("*.py"))
    if not files:
        raise AssertionError(f"no Python files in {ROOT / folder}")
    return [
        f"{path.relative_to(ROOT.parent)}:{line}: {module}{' (' + ', '.join(names) + ')' if names else ''}"
        for path in files
        for line, module, names in imports(path)
        if bad(module, names)
    ]


class LayerTest(unittest.TestCase):
    def test_algo_imports_only_the_standard_library_numpy_and_itself(self):
        self.assertEqual(violations("algo", lambda module, _: not algo_allowed(module)), [])

    def test_ops_does_not_import_the_worker(self):
        self.assertEqual(violations("ops", imports_worker), [])

    def test_relative_imports_resolve_against_the_package(self):
        # The rules see through relative imports: algo/'s own and a step out of it.
        fill = imports(ROOT / "algo" / "fill.py")
        self.assertIn(f"{PACKAGE}.algo.cards", [module for _, module, _ in fill])
        brief = imports(ROOT / "ops" / "brief.py")
        self.assertIn(f"{PACKAGE}.algo", [module for _, module, _ in brief])
        self.assertFalse(algo_allowed(PACKAGE))
        self.assertFalse(algo_allowed("requests"))
        self.assertTrue(imports_worker(PACKAGE, ("worker",)))


if __name__ == "__main__":
    unittest.main()
