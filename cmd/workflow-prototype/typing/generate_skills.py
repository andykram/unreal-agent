#!/usr/bin/env python3
"""Infer reviewable skill call builders from explicitly selected SKILL.md files."""
import argparse
import ast
import hashlib
import json
import keyword
from pathlib import Path
import re


TYPE_NAMES = {"string": "str", "str": "str", "path": "str", "integer": "int",
              "int": "int", "number": "float", "float": "float", "boolean": "bool", "bool": "bool"}
RESERVED = {"SkillCall", "SKILL_METADATA", "_make", "_json", "_shlex", "TypedDict", "Any",
            "str", "int", "float", "bool", "type", "zip", "any", "TypeError", "ValueError"}


def identifier(name):
    value = re.sub(r"[^a-zA-Z0-9_]", "_", name)
    if not value or value[0].isdigit() or keyword.iskeyword(value) or value.startswith("_"):
        value = "skill_" + value
    return value


def frontmatter(text):
    """Read common scalar fields only; this is deliberately not a YAML parser."""
    lines = text.splitlines()
    if not lines or lines[0] != "---":
        raise ValueError("SKILL.md must start with YAML frontmatter")
    try:
        end = lines.index("---", 1)
    except ValueError:
        raise ValueError("unterminated frontmatter") from None
    fields = {}
    index = 1
    relevant = {"name", "description", "argument-hint", "arguments"}
    while index < end:
        match = re.match(r"^([\w-]+):\s*(.*)$", lines[index])
        index += 1
        if not match or match[1] not in relevant:
            continue
        key, value = match.groups()
        if key in fields:
            raise ValueError(f"duplicate frontmatter key {key}")
        if value in ("|", ">", "|-", ">-", "|+", ">+"):
            parts = []
            while index < end and (lines[index].startswith((" ", "\t")) or not lines[index]):
                parts.append(lines[index].strip())
                index += 1
            value = ("\n" if value.startswith("|") else " ").join(parts)
        elif key == "arguments" and value.startswith("[") and value.endswith("]"):
            value = " ".join(part.strip().strip("\"'") for part in value[1:-1].split(","))
        elif key == "arguments" and not value:
            parts = []
            while index < end and re.match(r"^\s+-\s+", lines[index]):
                parts.append(re.sub(r"^\s+-\s+", "", lines[index]).strip().strip("\"'"))
                index += 1
            value = " ".join(parts)
        elif value.startswith('"'):
            try:
                value = json.loads(value)
            except json.JSONDecodeError as error:
                raise ValueError(f"unsupported quoted YAML scalar for {key}: {error}") from error
        elif value.startswith("'") and value.endswith("'"):
            value = value[1:-1].replace("''", "'")
        else:
            value = value.split(" #", 1)[0].rstrip()
            if value.startswith(("[", "{", "&", "*", "!")) and key != "argument-hint":
                raise ValueError(f"unsupported YAML scalar for {key}")
        fields[key] = value
    if not fields.get("name") or not fields.get("description"):
        raise ValueError("name and description are required")
    return fields, "\n".join(lines[end + 1:])


def parameter_table(body):
    """Only explicit Parameters/Arguments tables with Name, Type, Required columns."""
    section = False
    headers = None
    result = {}
    for line in body.splitlines():
        if line.startswith("#"):
            section = bool(re.match(r"^#{1,6}\s+(Parameters|Arguments)\s*$", line, re.I))
            headers = None
        if not section or not line.strip().startswith("|"):
            continue
        cells = [cell.strip().strip("`") for cell in line.strip().strip("|").split("|")]
        lowered = [cell.lower() for cell in cells]
        if {"name", "type", "required"}.issubset(lowered):
            headers = lowered
            continue
        if headers is None or len(cells) != len(headers) or all(re.fullmatch(r"[-: ]+", c) for c in cells):
            continue
        row = dict(zip(headers, cells))
        name = row["name"].lstrip("-")
        if row["type"].lower() not in TYPE_NAMES or row["required"].lower() not in {"yes", "no", "true", "false"}:
            continue
        if name in result:
            raise ValueError(f"duplicate parameter table entry {name}")
        result[name] = {"type": TYPE_NAMES[row["type"].lower()],
                        "required": row["required"].lower() in {"yes", "true"}, "evidence": line.strip()}
    return result


def parse_pattern(pattern, bracket_optional):
    # Accept complete sequences of placeholders and optional --flag groups only.
    token = r"(?:<([\w-]+)>|\[([\w-]+)\]|\[(--[\w-]+)\s+<([\w-]+)>\])"
    matches = list(re.finditer(token, pattern))
    if not matches or re.sub(token, "", pattern).strip():
        return None
    params = []
    for match in matches:
        required_name, bracket_name, flag, flag_name = match.groups()
        params.append({"name": required_name or bracket_name or flag_name,
                       "type": "str", "required": bool(required_name) or (bool(bracket_name) and not bracket_optional),
                       "flag": flag})
    return params


def infer(path):
    text = path.read_text(encoding="utf-8")
    fields, body = frontmatter(text)
    evidence, params, confidence = [], None, "low"
    reason = "No unambiguous invocation contract; accepts arbitrary string arguments."
    if fields.get("arguments"):
        names = fields["arguments"].split()
        if all(re.fullmatch(r"[\w-]+", name) for name in names):
            params = [{"name": name, "type": "str", "required": True, "flag": None} for name in names]
            evidence = ["frontmatter arguments: " + fields["arguments"]]
            confidence = "medium"
    elif fields.get("argument-hint"):
        # Claude hints use brackets as presentation, not a requiredness contract.
        params = parse_pattern(fields["argument-hint"], bracket_optional=False)
        evidence = ["frontmatter argument-hint: " + fields["argument-hint"]]
        confidence = "medium" if params is not None else "low"
    else:
        patterns = []
        for line in body.splitlines():
            line = line.strip().strip("`")
            match = re.fullmatch(r"/" + re.escape(fields["name"]) + r"\s+(.+)", line)
            if match and match[1] not in patterns:
                patterns.append(match[1])
        if len(patterns) == 1:
            params = parse_pattern(patterns[0], bracket_optional=True)
            evidence = ["documented slash invocation: /" + fields["name"] + " " + patterns[0]]
            confidence = "medium" if params is not None else "low"
        if params is None:
            positions = {int(a or b) for a, b in re.findall(r"\$ARGUMENTS\[(\d+)\]|\$(\d+)\b", body)}
            if positions and max(positions) < 32:
                params = [{"name": f"arg{index}", "type": "str", "required": True, "flag": None}
                          for index in range(max(positions) + 1)]
                evidence = ["indexed placeholders: " + ", ".join(f"${index}" for index in sorted(positions))]
                confidence = "medium"
            elif "$ARGUMENTS" in body:
                evidence = ["$ARGUMENTS catch-all placeholder"]
    table = parameter_table(body)
    if params is None and table:
        params = [{"name": name, "type": info["type"], "required": info["required"], "flag": None}
                  for name, info in table.items()]
        evidence = ["Parameters/Arguments table order interpreted as positional order"]
        confidence = "low"
    if params is not None:
        seen = set()
        for param in params:
            if param["name"] in table:
                info = table[param["name"]]
                param.update(type=info["type"], required=info["required"])
                evidence.append(info["evidence"])
            param["python_name"] = identifier(param["name"])
            if param["python_name"] in seen:
                raise ValueError(f"parameter identifier collision: {param['python_name']}")
            seen.add(param["python_name"])
        reason = "Heuristic signature; review inferred parameter order, types, and requiredness."
    return {"name": fields["name"], "description": fields["description"],
            "callable": identifier(fields["name"]), "parameters": params,
            "source": str(path), "sha256": hashlib.sha256(text.encode()).hexdigest(),
            "inference": {"confidence": confidence, "evidence": evidence, "note": reason}}


RUNTIME = '''# Generated skill descriptors. No skill content is executed.
import json as _json
import shlex as _shlex
from typing import Any, TypedDict

class SkillCall(TypedDict):
    name: str
    arguments: str

SKILL_METADATA = _json.loads(METADATA_JSON)

def _make(name, values):
    params = SKILL_METADATA[name]["parameters"]
    if params is None:
        if any(type(value) is not str for value in values):
            raise TypeError("skill arguments must be strings")
        return {"name": name, "arguments": _shlex.join(values)}
    args = []
    missing = False
    for param, value in zip(params, values):
        if value is None:
            if param["required"]:
                raise TypeError("missing required argument: " + param["name"])
            if param["flag"] is None:
                missing = True
            continue
        if missing and param["flag"] is None:
            raise ValueError("cannot omit an earlier positional argument")
        expected = {"str": str, "int": int, "float": float, "bool": bool}[param["type"]]
        if type(value) is not expected and not (expected is float and type(value) is int):
            raise TypeError("invalid type for " + param["name"])
        if param["flag"]:
            args.append(param["flag"])
        args.append(str(value).lower() if type(value) is bool else str(value))
    return {"name": name, "arguments": _shlex.join(args)}

'''


def generate(skills):
    metadata, used = {}, set(RESERVED)
    runtime, stub = [], ['from typing import Any, TypedDict\n\nclass SkillCall(TypedDict):\n    name: str\n    arguments: str\n\nSKILL_METADATA: dict[str, Any]\n']
    for skill in sorted(skills, key=lambda skill: skill["name"]):
        name, function = skill["name"], skill["callable"]
        if name in metadata or function in used:
            raise ValueError(f"duplicate name or generated callable collision: {name!r} -> {function!r}")
        used.add(function)
        metadata[name] = skill
        params = skill["parameters"]
        if params is None:
            signature, values = "*arguments: str", "arguments"
        else:
            # Keyword-only arguments avoid required-after-optional syntax and make inferred names visible.
            signature = "*, " + ", ".join(p["python_name"] + ": " + p["type"] +
                (" | None = None" if not p["required"] else "") for p in params)
            values = "[" + ", ".join(p["python_name"] for p in params) + "]"
        declaration = f"def {function}({signature}) -> SkillCall:"
        runtime.append(declaration + "\n    " + repr(skill["description"]) +
                       f"\n    return _make({name!r}, {values})\n")
        stub.append(declaration + " ...\n")
    source = RUNTIME.replace("METADATA_JSON", repr(json.dumps(metadata, sort_keys=True, ensure_ascii=True))) + "\n".join(runtime)
    stubs = "\n".join(stub)
    ast.parse(source)
    ast.parse(stubs)
    return {"generated_skills.py": source, "generated_skills.pyi": stubs,
            "metadata.json": json.dumps(metadata, indent=2, sort_keys=True, ensure_ascii=True) + "\n"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("skills", type=Path, nargs="+")
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        outputs = generate([infer(path) for path in args.skills])
        if any((args.output_dir / name).exists() for name in outputs):
            raise ValueError("output files already exist; generate into a fresh directory")
        args.output_dir.mkdir(parents=True, exist_ok=True)
        for name, content in outputs.items():
            with (args.output_dir / name).open("x", encoding="utf-8", newline="\n") as output:
                output.write(content)
    except (OSError, ValueError, SyntaxError) as error:
        parser.exit(1, f"error: {error}\n")


if __name__ == "__main__":
    main()
