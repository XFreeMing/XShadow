from domain import SquadId, Task


def unclaimed_task():
    return Task("T-1"), SquadId("A"), SquadId("B")
