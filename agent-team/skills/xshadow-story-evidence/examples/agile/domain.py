from dataclasses import dataclass, field
from typing import Protocol


@dataclass(frozen=True)
class SquadId:
    value: str


class TaskOwnership(Protocol):
    @property
    def assigned_squad(self) -> SquadId | None: ...

    def claim(self, squad: SquadId) -> bool: ...


@dataclass(eq=False)
class Task:
    task_id: str
    _assigned_squad: SquadId | None = field(default=None, init=False)

    @property
    def assigned_squad(self) -> SquadId | None:
        return self._assigned_squad

    def claim(self, squad: SquadId) -> bool:
        if self._assigned_squad is not None:
            return False
        self._assigned_squad = squad
        return True
