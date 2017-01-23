import "0.wdl"
import "2.wdl" as ns

Float pi = 3.14

task y {
  command { echo ${pi} }
}

workflow wf {
  call y
  call ns.x
}
