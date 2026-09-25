#!/usr/bin/env python3
"""Generate editor types from an explicit skill catalog snapshot (Python 3.11+)."""

import argparse
import ast
import json
from pathlib import Path


def generate(catalog):
    if not isinstance(catalog, list):
        raise ValueError("catalog must be a list of {name, description} objects")
    names = set()
    for item in catalog:
        if not isinstance(item, dict):
            raise ValueError("each catalog entry must be an object")
        name, description = item.get("name"), item.get("description")
        if not isinstance(name, str) or not name.strip():
            raise ValueError("each skill needs a nonempty name")
        if not isinstance(description, str):
            raise ValueError("each skill needs a string description")
        if name in names:
            raise ValueError(f"duplicate skill name: {name!r}")
        names.add(name)
    literals = ", ".join(json.dumps(name, ensure_ascii=True) for name in sorted(names))
    skill_type = f"Literal[{literals}]" if names else "Never"
    return '''# Generated from a skill catalog snapshot. Do not edit.
from dataclasses import dataclass
from typing import Any, Iterable, Literal, Never, Self, Sequence, TypeAlias, TypedDict
from pydantic import BaseModel

SkillName: TypeAlias = ''' + skill_type + '''
Dependencies: TypeAlias = str | Sequence[str]
PathPart: TypeAlias = str | int
JSONScalar: TypeAlias = str | bool | int | float | None

class SkillCall(TypedDict):
    name: str
    arguments: str

Skill: TypeAlias = SkillName | SkillCall

class OutcomeCondition(TypedDict):
    step: str
    outcome: Literal["passed", "failed"]

class FieldCondition(TypedDict):
    step: str
    path: list[PathPart]
    equals: JSONScalar

Condition: TypeAlias = OutcomeCondition | FieldCondition

class OutputModel(BaseModel):
    @classmethod
    def model_json_schema(cls, **kwargs: Any) -> dict[str, Any]: ...
    @classmethod
    def model_validate_json(cls, text: str | bytes | bytearray, **kwargs: Any) -> Self: ...
    def model_dump(self, **kwargs: Any) -> dict[str, Any]: ...

def output_schema(model: type[BaseModel]) -> dict[str, Any]: ...

@dataclass(frozen=True)
class OutputRef:
    step: str
    path: tuple[PathPart, ...] = ()
    def field(self, *path: PathPart) -> OutputRef: ...
    def equals(self, value: JSONScalar) -> FieldCondition: ...

@dataclass(frozen=True)
class Agent:
    prompt: str
    output: type[BaseModel]
    skills: tuple[Skill, ...] = ()
    system_prompt_append: str = ""
    def bind(self, flow: Workflow, id: str, *, workspace: str,
             inputs: dict[str, Any] | None = None, after: Dependencies = (),
             when: Condition | None = None) -> str: ...

@dataclass(frozen=True)
class Pipeline:
    steps: tuple[str, ...]
    @property
    def last(self) -> str: ...
    @property
    def output(self) -> OutputRef: ...

class Workflow:
    name: str
    steps: list[dict[str, Any]]
    def __init__(self, name: str) -> None: ...
    def step(self, id: str, kind: str, *, after: Dependencies = (),
             when: Condition | None = None, **spec: Any) -> str: ...
    def worktree(self, id: str, base: str = "current") -> str: ...
    def agent(self, id: str, *, workspace: str, prompt: str,
              after: Dependencies = (), when: Condition | None = None,
              skills: Sequence[Skill] = (), output: type[BaseModel] | None = None,
              inputs: dict[str, Any] | None = None, system_prompt_append: str = "") -> str: ...
    def command(self, id: str, argv: Sequence[str], *, workspace: str,
                after: Dependencies, when: Condition | None = None) -> str: ...
    def repeat_check(self, id: str, argv: Sequence[str], *, workspace: str,
                     repair_prompt: str, max_repairs: int = 2,
                     after: Dependencies, when: Condition | None = None,
                     repair_skills: Sequence[Skill] = (),
                     repair_system_prompt_append: str = "") -> str: ...
    def approval(self, id: str, *, after: Dependencies,
                 when: Condition | None = None) -> str: ...
    def join(self, id: str, *, after: Dependencies) -> str: ...
    def ref(self, step: str, *path: PathPart) -> OutputRef: ...
    def chain(self, prefix: str, stages: Iterable[tuple[str, Agent]], *,
               workspace: str, inputs: dict[str, Any] | None = None,
               context: Any = None, after: Dependencies = ()) -> Pipeline: ...
    def map_agents(self, prefix: str, items: Iterable[Any], agent: Agent, *,
                    workspaces: Iterable[str], after: Dependencies = ()) -> list[str]: ...
    def reduce_agent(self, id: str, steps: Iterable[str], agent: Agent, *,
                      workspace: str, inputs: dict[str, Any] | None = None,
                      after: Dependencies = ()) -> str: ...
    @staticmethod
    def _skill_names(names: Iterable[Skill]) -> list[Skill]: ...
    def export(self) -> None: ...
'''



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("catalog", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.suffix != ".pyi":
        parser.error("--output must name a .pyi file")
    try:
        source = generate(json.loads(args.catalog.read_text(encoding="utf-8")))
        ast.parse(source)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        # Exclusive creation avoids overwriting SDK files or a previous snapshot.
        with args.output.open("x", encoding="utf-8", newline="\n") as output:
            output.write(source)
    except (OSError, ValueError, SyntaxError) as error:
        parser.exit(1, f"error: {error}\n")


if __name__ == "__main__":
    main()
